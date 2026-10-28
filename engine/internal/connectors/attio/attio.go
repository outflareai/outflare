// Package attio connects Attio so a workspace can move its CRM into
// Outflare, once. It only reads: a preview of what an import would bring
// over (objects, fields, lists and how much is in each), then the members
// and records the import copies into Outflare's customer records. Nothing
// is written to Attio, and there is no ongoing sync.
//
// Two ways to connect, one connector: OAuth (needs an Attio app configured
// on the server) or an access token from the workspace's own Attio
// developer settings.
package attio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const api = "https://api.attio.com"

// Page sizes Attio allows: 500 for records, entries and tasks, 50 for notes.
const (
	pageSize     = 500
	notePageSize = 50
)

// The preview reads a whole Attio workspace while someone waits for it, so
// it makes up to previewWorkers calls at once (Attio allows 100 reads a
// second) and stops counting after previewTime, marking those counts capped.
const previewWorkers = 8

var previewTime = 15 * time.Second

// All returns Attio.
func All() []c.Connector { return []c.Connector{Attio{}} }

// Attio reads a workspace's Attio setup.
type Attio struct{}

func (Attio) Definition() c.Definition {
	return c.Definition{
		ID: "attio", Name: "Attio", Category: c.CategoryCRM,
		Summary: "Bring your Attio CRM over: see every object, field and list it holds, then import it once.",
		Description: "Reads your Attio workspace's objects (People, Companies, Deals and custom ones), their fields, lists and pipelines, " +
			"and counts records, entries, notes and tasks, so you can see what moving over brings before anything is copied. " +
			"Read-only: Outflare never changes anything in Attio.",
		Website: "https://attio.com", DocsURL: "https://docs.attio.com/rest-api/overview",
		KeyURL: "https://attio.com/help/apps/other-apps/generating-an-api-key",
		Auth:   c.AuthOAuth2, AuthMethods: c.Both,
		// Attio sets an app's scopes in its developer dashboard, not on the
		// consent screen.
		OAuth: &c.OAuth{AuthURL: "https://app.attio.com/authorize", TokenURL: "https://app.attio.com/oauth/token", Scopes: []string{}},
		Fields: []c.Field{{Name: "api_key", Label: "Access token", Secret: true,
			Help: "In Attio: Workspace settings, Developers, New access token. Give it read access to records, object configuration, lists, " +
				"list entries, list configuration, notes, tasks and workspace members. Read access is enough."}},
		Capabilities: []string{c.CapImportCRM},
		Pricing:      "Works on every Attio plan, including Free",
	}
}

// get calls Attio and decodes the answer into out.
func get(ctx context.Context, cl *c.Client, creds c.Creds, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, cl.Base("attio", api)+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.Token(creds))
	_, err := c.DoJSON(ctx, cl, "Attio", req, out)
	return err
}

func (Attio) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var self struct {
		Active        bool   `json:"active"`
		WorkspaceID   string `json:"workspace_id"`
		WorkspaceName string `json:"workspace_name"`
		WorkspaceSlug string `json:"workspace_slug"`
	}
	if err := get(ctx, cl, creds, http.MethodGet, "/v2/self", nil, &self); err != nil {
		return c.Account{}, err
	}
	if !self.Active {
		return c.Account{}, c.Errf(c.ErrAuth, "Attio says this token is no longer active. Create a new one, or connect again.")
	}
	return c.Account{ID: self.WorkspaceID, Name: self.WorkspaceName, Details: map[string]string{"workspace_slug": self.WorkspaceSlug}}, nil
}

type attribute struct {
	Slug        string `json:"api_slug"`
	Title       string `json:"title"`
	Type        string `json:"type"`
	System      bool   `json:"is_system_attribute"`
	Multiselect bool   `json:"is_multiselect"`
	Unique      bool   `json:"is_unique"`
	Required    bool   `json:"is_required"`
	Archived    bool   `json:"is_archived"`
	Config      struct {
		RecordReference struct {
			AllowedObjectIDs []string `json:"allowed_object_ids"`
		} `json:"record_reference"`
	} `json:"config"`
}

// kinds maps Attio's attribute types to CRM field kinds. interaction and
// anything new are computed by Attio, so they are not imported.
var kinds = map[string]string{
	"text": c.CRMText, "number": c.CRMNumber, "checkbox": c.CRMCheckbox, "currency": c.CRMCurrency, "date": c.CRMDate,
	"timestamp": c.CRMTimestamp, "rating": c.CRMRating, "select": c.CRMSelect, "status": c.CRMStatus,
	"email-address": c.CRMEmail, "phone-number": c.CRMPhone, "domain": c.CRMDomain, "record-reference": c.CRMReference,
	"actor-reference": c.CRMMemberRef, "location": c.CRMLocationVal, "personal-name": c.CRMPersonName,
}

type titled struct {
	Title    string `json:"title"`
	Archived bool   `json:"is_archived"`
}

// fields reads the attributes at base (an object's or a list's), with
// select options and status stages.
func fields(ctx context.Context, cl *c.Client, creds c.Creds, base string, objectSlugs map[string]string) ([]c.CRMField, error) {
	var resp struct {
		Data []attribute `json:"data"`
	}
	if err := get(ctx, cl, creds, http.MethodGet, base+"/attributes", nil, &resp); err != nil {
		return nil, err
	}
	out := []c.CRMField{}
	for _, a := range resp.Data {
		if a.Archived {
			continue
		}
		f := c.CRMField{Slug: a.Slug, Title: a.Title, Type: a.Type, Kind: kinds[a.Type], System: a.System, Multiple: a.Multiselect, Unique: a.Unique, Required: a.Required}
		if f.Kind == "" {
			f.Kind = c.CRMUnsupported
		}
		for _, id := range a.Config.RecordReference.AllowedObjectIDs {
			if s, ok := objectSlugs[id]; ok {
				f.Target = append(f.Target, s)
			}
		}
		if kind := map[string]string{"status": "statuses", "select": "options"}[a.Type]; kind != "" {
			var opts struct {
				Data []titled `json:"data"`
			}
			if err := get(ctx, cl, creds, http.MethodGet, base+"/attributes/"+url.PathEscape(a.Slug)+"/"+kind, nil, &opts); err != nil {
				return nil, err
			}
			for _, o := range opts.Data {
				if !o.Archived {
					f.Options = append(f.Options, o.Title)
				}
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// count pages through query results up to limit, or until stop, counting how often each
// field is filled. key is "values" for records and "entry_values" for
// list entries.
func count(ctx context.Context, cl *c.Client, creds c.Creds, path, key string, limit int, stop time.Time, fs []c.CRMField) (c.CRMCount, error) {
	idx := map[string]int{}
	for i, f := range fs {
		idx[f.Slug] = i
	}
	var n c.CRMCount
	for offset := 0; ; offset += pageSize {
		if offset >= limit || offset > 0 && time.Now().After(stop) {
			n.Capped = true
			return n, nil
		}
		size := min(pageSize, limit-offset)
		var resp struct {
			Data []map[string]json.RawMessage `json:"data"`
		}
		if err := get(ctx, cl, creds, http.MethodPost, path, map[string]int{"limit": size, "offset": offset}, &resp); err != nil {
			return n, err
		}
		for _, row := range resp.Data {
			n.Count++
			var values map[string][]json.RawMessage
			_ = json.Unmarshal(row[key], &values)
			for slug, v := range values {
				if i, ok := idx[slug]; ok && len(v) > 0 {
					fs[i].Filled++
				}
			}
		}
		if len(resp.Data) < size {
			return n, nil
		}
	}
}

// countPages counts a plain list endpoint (notes, tasks) up to limit.
func countPages(ctx context.Context, cl *c.Client, creds c.Creds, path string, size, limit int, stop time.Time) (c.CRMCount, error) {
	var n c.CRMCount
	for offset := 0; ; offset += size {
		if offset >= limit || offset > 0 && time.Now().After(stop) {
			n.Capped = true
			return n, nil
		}
		want := min(size, limit-offset)
		var resp struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := get(ctx, cl, creds, http.MethodGet, fmt.Sprintf("%s?limit=%d&offset=%d", path, want, offset), nil, &resp); err != nil {
			return n, err
		}
		n.Count += len(resp.Data)
		if len(resp.Data) < want {
			return n, nil
		}
	}
}

// CRMPreview reads the workspace's objects, lists, members, notes and
// tasks. Each count stops at limit, or once the preview has run for
// previewTime, and then says it is capped.
func (a Attio) CRMPreview(ctx context.Context, cl *c.Client, creds c.Creds, limit int) (c.CRMPreview, error) {
	acct, err := a.Test(ctx, cl, creds)
	if err != nil {
		return c.CRMPreview{}, err
	}
	out := c.CRMPreview{Workspace: acct.Name, Objects: []c.CRMObject{}, Lists: []c.CRMList{}}

	var objects struct {
		Data []struct {
			ID struct {
				ObjectID string `json:"object_id"`
			} `json:"id"`
			Slug     string `json:"api_slug"`
			Singular string `json:"singular_noun"`
			Plural   string `json:"plural_noun"`
		} `json:"data"`
	}
	if err := get(ctx, cl, creds, http.MethodGet, "/v2/objects", nil, &objects); err != nil {
		return out, err
	}
	var lists struct {
		Data []struct {
			Slug   string   `json:"api_slug"`
			Name   string   `json:"name"`
			Parent []string `json:"parent_object"`
		} `json:"data"`
	}
	if err := get(ctx, cl, creds, http.MethodGet, "/v2/lists", nil, &lists); err != nil {
		return out, err
	}
	var members struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := get(ctx, cl, creds, http.MethodGet, "/v2/workspace_members", nil, &members); err != nil {
		return out, err
	}
	out.Members = len(members.Data)

	slugs := map[string]string{}
	for _, o := range objects.Data {
		slugs[o.ID.ObjectID] = o.Slug
	}
	stop := time.Now().Add(previewTime)
	g := newGroup(ctx, previewWorkers)
	out.Objects = make([]c.CRMObject, len(objects.Data))
	for i, o := range objects.Data {
		g.Go(func(ctx context.Context) error {
			base := "/v2/objects/" + url.PathEscape(o.Slug)
			fs, err := fields(ctx, cl, creds, base, slugs)
			if err != nil {
				return err
			}
			n, err := count(ctx, cl, creds, base+"/records/query", "values", limit, stop, fs)
			out.Objects[i] = c.CRMObject{Slug: o.Slug, Name: o.Singular, Plural: o.Plural, Records: n, Fields: fs}
			return err
		})
	}
	out.Lists = make([]c.CRMList, len(lists.Data))
	for i, l := range lists.Data {
		g.Go(func(ctx context.Context) error {
			base := "/v2/lists/" + url.PathEscape(l.Slug)
			fs, err := fields(ctx, cl, creds, base, slugs)
			if err != nil {
				return err
			}
			n, err := count(ctx, cl, creds, base+"/entries/query", "entry_values", limit, stop, fs)
			out.Lists[i] = c.CRMList{Slug: l.Slug, Name: l.Name, Entries: n, Fields: fs}
			if len(l.Parent) > 0 {
				out.Lists[i].Object = l.Parent[0]
			}
			return err
		})
	}
	g.Go(func(ctx context.Context) (err error) {
		out.Notes, err = countPages(ctx, cl, creds, "/v2/notes", notePageSize, limit, stop)
		return err
	})
	g.Go(func(ctx context.Context) (err error) {
		out.Tasks, err = countPages(ctx, cl, creds, "/v2/tasks", pageSize, limit, stop)
		return err
	})
	return out, g.Wait()
}

// group runs functions with at most n at once. The first error cancels the
// rest and is what Wait returns.
type group struct {
	ctx    context.Context
	cancel context.CancelFunc
	sem    chan struct{}
	wg     sync.WaitGroup
	once   sync.Once
	err    error
}

func newGroup(ctx context.Context, n int) *group {
	ctx, cancel := context.WithCancel(ctx)
	return &group{ctx: ctx, cancel: cancel, sem: make(chan struct{}, n)}
}

func (g *group) Go(f func(context.Context) error) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		select {
		case g.sem <- struct{}{}:
		case <-g.ctx.Done():
			return
		}
		defer func() { <-g.sem }()
		if err := f(g.ctx); err != nil {
			g.once.Do(func() { g.err = err; g.cancel() })
		}
	}()
}

func (g *group) Wait() error {
	g.wg.Wait()
	g.cancel()
	return g.err
}

func (Attio) CRMMembers(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.CRMMember, error) {
	var resp struct {
		Data []struct {
			ID struct {
				MemberID string `json:"workspace_member_id"`
			} `json:"id"`
			Email string `json:"email_address"`
		} `json:"data"`
	}
	if err := get(ctx, cl, creds, http.MethodGet, "/v2/workspace_members", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]c.CRMMember, 0, len(resp.Data))
	for _, m := range resp.Data {
		out = append(out, c.CRMMember{ID: m.ID.MemberID, Email: m.Email})
	}
	return out, nil
}

// value is any one Attio attribute value; which fields are set depends on
// the attribute's type.
type value struct {
	Value         json.RawMessage `json:"value"`
	Option        *titled         `json:"option"`
	Status        *titled         `json:"status"`
	Email         string          `json:"email_address"`
	Phone         string          `json:"phone_number"`
	Domain        string          `json:"domain"`
	CurrencyValue *float64        `json:"currency_value"`
	CurrencyCode  string          `json:"currency_code"`
	TargetObject  string          `json:"target_object"`
	TargetID      string          `json:"target_record_id"`
	ActorType     string          `json:"referenced_actor_type"`
	ActorID       string          `json:"referenced_actor_id"`
	Line1         string          `json:"line_1"`
	Line2         string          `json:"line_2"`
	Locality      string          `json:"locality"`
	Region        string          `json:"region"`
	Postcode      string          `json:"postcode"`
	CountryCode   string          `json:"country_code"`
	FirstName     string          `json:"first_name"`
	LastName      string          `json:"last_name"`
	FullName      string          `json:"full_name"`
}

// convert turns an Attio value into the Go type its kind uses, or nil when
// it holds nothing usable.
func (v value) convert() any {
	scalar := func() any {
		var x any
		if len(v.Value) == 0 || json.Unmarshal(v.Value, &x) != nil {
			return nil
		}
		return x
	}
	switch {
	case v.Option != nil:
		return v.Option.Title
	case v.Status != nil:
		return v.Status.Title
	case v.Email != "":
		return v.Email
	case v.Phone != "":
		return v.Phone
	case v.Domain != "":
		return v.Domain
	case v.CurrencyValue != nil:
		return c.CRMMoney{Amount: *v.CurrencyValue, Currency: v.CurrencyCode}
	case v.TargetObject != "" && v.TargetID != "":
		return c.CRMRef{Object: v.TargetObject, ID: v.TargetID}
	case v.ActorID != "":
		if v.ActorType != "workspace-member" {
			return nil
		}
		return v.ActorID
	case v.FirstName != "" || v.LastName != "" || v.FullName != "":
		return c.CRMName{First: v.FirstName, Last: v.LastName, Full: v.FullName}
	case v.Line1 != "" || v.Locality != "" || v.Region != "" || v.Postcode != "" || v.CountryCode != "":
		return c.CRMLocation{Line1: v.Line1, Line2: v.Line2, City: v.Locality, Region: v.Region, PostalCode: v.Postcode, Country: v.CountryCode}
	}
	return scalar()
}

// CRMRecords reads a page of an object's records.
func (Attio) CRMRecords(ctx context.Context, cl *c.Client, creds c.Creds, object string, offset, limit int) ([]c.CRMRecord, error) {
	var resp struct {
		Data []struct {
			ID struct {
				RecordID string `json:"record_id"`
			} `json:"id"`
			Values map[string][]value `json:"values"`
		} `json:"data"`
	}
	body := map[string]any{"limit": limit, "offset": offset, "sorts": []map[string]string{{"attribute": "created_at", "direction": "asc"}}}
	if err := get(ctx, cl, creds, http.MethodPost, "/v2/objects/"+url.PathEscape(object)+"/records/query", body, &resp); err != nil {
		return nil, err
	}
	out := make([]c.CRMRecord, 0, len(resp.Data))
	for _, r := range resp.Data {
		rec := c.CRMRecord{ID: r.ID.RecordID, Values: map[string][]any{}}
		for slug, vs := range r.Values {
			for _, v := range vs {
				if x := v.convert(); x != nil {
					rec.Values[slug] = append(rec.Values[slug], x)
				}
			}
		}
		out = append(out, rec)
	}
	return out, nil
}
