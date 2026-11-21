package ads

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// A live read of any Google Ads data: one resource, its fields, conditions
// and orderings, put together as a Google Ads Query Language SELECT and
// sent to the search endpoint, which can only read. Each part is checked
// so that a condition can't smuggle in another clause (a larger LIMIT, a
// second query).

var _ c.AdQuerier = GoogleAds{}

var (
	gaqlResource = regexp.MustCompile(`^[a-z][a-z_]{1,80}$`)
	gaqlField    = regexp.MustCompile(`^[a-z][a-z_]*(\.[a-z][a-z0-9_]*){1,6}$`)
	gaqlOrder    = regexp.MustCompile(`^[a-z][a-z_]*(\.[a-z][a-z0-9_]*){1,6}( (ASC|DESC))?$`)
	gaqlCond     = regexp.MustCompile(`^[a-z][a-z_]*(\.[a-z][a-z0-9_]*){1,6} +(=|!=|>=|<=|>|<|IN|NOT IN|LIKE|NOT LIKE|CONTAINS ANY|CONTAINS ALL|CONTAINS NONE|IS NULL|IS NOT NULL|DURING|BETWEEN|REGEXP_MATCH|NOT REGEXP_MATCH)( .*)?$`)
	gaqlQuoted   = regexp.MustCompile(`'[^']*'|"[^"]*"`)
	gaqlClause   = regexp.MustCompile(`(?i)\b(select|from|where|order\s+by|limit|parameters)\b`)
)

// googleQuery is q as one GAQL statement.
func googleQuery(q c.AdQuery) (string, int, error) {
	if !gaqlResource.MatchString(q.Resource) {
		return "", 0, c.Errf(c.ErrInvalid, "resource is one Google Ads resource in lowercase, such as campaign or ad_group_criterion")
	}
	if len(q.Fields) == 0 || len(q.Fields) > 80 {
		return "", 0, c.Errf(c.ErrInvalid, "fields names 1 to 80 fields, such as campaign.id")
	}
	for _, f := range q.Fields {
		if !gaqlField.MatchString(f) {
			return "", 0, c.Errf(c.ErrInvalid, "%q is not a field: write it as resource.field, such as campaign.name or metrics.search_impression_share", f)
		}
	}
	if len(q.Conditions) > 20 || len(q.Orderings) > 5 {
		return "", 0, c.Errf(c.ErrInvalid, "a query takes at most 20 conditions and 5 orderings")
	}
	for _, cond := range q.Conditions {
		bare := gaqlQuoted.ReplaceAllString(cond, "''")
		if len(cond) > 500 || strings.ContainsAny(cond, ";\n\r") || !gaqlCond.MatchString(cond) || gaqlClause.MatchString(bare) || strings.Count(bare, "'")%2 != 0 || strings.Count(bare, `"`)%2 != 0 {
			return "", 0, c.Errf(c.ErrInvalid, "%q is not a condition: write one comparison, such as campaign.status = 'ENABLED' or segments.date DURING LAST_30_DAYS", cond)
		}
	}
	for _, o := range q.Orderings {
		if !gaqlOrder.MatchString(o) {
			return "", 0, c.Errf(c.ErrInvalid, "%q is not an ordering: write a field, then ASC or DESC, such as metrics.clicks DESC", o)
		}
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > c.AdQueryMaxRows {
		return "", 0, c.Errf(c.ErrInvalid, "limit is at most %d", c.AdQueryMaxRows)
	}
	var b strings.Builder
	b.WriteString("SELECT " + strings.Join(q.Fields, ", ") + " FROM " + q.Resource)
	if len(q.Conditions) > 0 {
		b.WriteString(" WHERE " + strings.Join(q.Conditions, " AND "))
	}
	if len(q.Orderings) > 0 {
		b.WriteString(" ORDER BY " + strings.Join(q.Orderings, ", "))
	}
	// One more than asked for tells a full answer from a cut one.
	b.WriteString(" LIMIT " + strconv.Itoa(limit+1))
	return b.String(), limit, nil
}

func (g GoogleAds) AdQuery(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, q c.AdQuery) (c.AdQueryResult, error) {
	query, limit, err := googleQuery(q)
	if err != nil {
		return c.AdQueryResult{}, err
	}
	var resp struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := g.call(cl, creds, acct.Meta["login_customer_id"]).post(ctx, g.base(cl)+"/customers/"+esc(acct.ID)+"/googleAds:search", map[string]any{"query": query}, &resp); err != nil {
		return c.AdQueryResult{}, err
	}
	out := c.AdQueryResult{Query: strings.TrimSuffix(query, strconv.Itoa(limit+1)) + strconv.Itoa(limit), Rows: resp.Results, Limit: limit, Complete: len(resp.Results) <= limit}
	if !out.Complete {
		out.Rows = out.Rows[:limit]
	}
	if out.Rows == nil {
		out.Rows = []json.RawMessage{}
	}
	out.RowCount = len(out.Rows)
	return out, nil
}

func (g GoogleAds) AdQueryFields(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, resource string) ([]c.AdQueryField, error) {
	// The field service's conditions join with AND only, so the resource
	// and its fields are two reads.
	wheres := []string{"category = 'RESOURCE'"}
	cols := "name, category, selectable, filterable, sortable"
	if resource != "" {
		if !gaqlResource.MatchString(resource) {
			return nil, c.Errf(c.ErrInvalid, "resource is one Google Ads resource in lowercase, such as campaign")
		}
		wheres = []string{"name = '" + resource + "'", "name LIKE '" + resource + ".%'"}
		cols = "name, category, data_type, selectable, filterable, sortable, is_repeated, enum_values, selectable_with"
	}
	var out []c.AdQueryField
	for _, where := range wheres {
		token := ""
		for i := 0; i < maxPages; i++ {
			body := map[string]any{"query": "SELECT " + cols + " WHERE " + where + " ORDER BY name"}
			if token != "" {
				body["pageToken"] = token
			}
			var resp struct {
				Results []struct {
					Name           string   `json:"name"`
					Category       string   `json:"category"`
					DataType       string   `json:"dataType"`
					Selectable     bool     `json:"selectable"`
					Filterable     bool     `json:"filterable"`
					Sortable       bool     `json:"sortable"`
					IsRepeated     bool     `json:"isRepeated"`
					EnumValues     []string `json:"enumValues"`
					SelectableWith []string `json:"selectableWith"`
				} `json:"results"`
				NextPageToken string `json:"nextPageToken"`
			}
			if err := g.call(cl, creds, acct.Meta["login_customer_id"]).post(ctx, g.base(cl)+"/googleAdsFields:search", body, &resp); err != nil {
				return nil, err
			}
			for _, f := range resp.Results {
				x := c.AdQueryField{Name: f.Name, Category: strings.ToLower(f.Category), DataType: strings.ToLower(f.DataType), Selectable: f.Selectable, Filterable: f.Filterable,
					Sortable: f.Sortable, Repeated: f.IsRepeated, EnumValues: f.EnumValues, SelectableWith: f.SelectableWith}
				if x.EnumValues == nil {
					x.EnumValues = []string{}
				}
				if x.SelectableWith == nil {
					x.SelectableWith = []string{}
				}
				out = append(out, x)
			}
			if token = resp.NextPageToken; token == "" {
				break
			}
		}
	}
	if resource != "" && len(out) == 0 {
		return nil, c.Errf(c.ErrInvalid, "Google Ads has no resource named %s; list them by leaving resource out", resource)
	}
	return out, nil
}
