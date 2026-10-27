// Package eventstore keeps a copy of product events in ClickHouse for
// analytics (docs/adr/0010-analytics-storage.md). Postgres stays the
// source of truth: a mirror copies every new, changed or deleted event from
// customer_events, and analytics queries read ClickHouse when it is set up.
// Without OUTFLARE_CLICKHOUSE_URL nothing here runs.
//
// ClickHouse is reached over its HTTP interface, with no client library.
package eventstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ClickHouse is a connection to one ClickHouse database.
type ClickHouse struct {
	endpoint string // scheme://host:port/
	user     string
	password string
	database string
	http     *http.Client
}

// Open parses a URL such as https://user:password@host:8443/outflare (the
// path, or ?database=, names the database; default "default").
func Open(raw string) (*ClickHouse, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("OUTFLARE_CLICKHOUSE_URL must be an http or https address, such as https://user:password@host:8443/outflare")
	}
	c := &ClickHouse{
		endpoint: u.Scheme + "://" + u.Host + "/",
		database: strings.Trim(u.Path, "/"),
		http:     &http.Client{Timeout: 60 * time.Second},
	}
	if db := u.Query().Get("database"); db != "" {
		c.database = db
	}
	if c.database == "" {
		c.database = "default"
	}
	if strings.ContainsAny(c.database, "/` .") {
		return nil, fmt.Errorf("OUTFLARE_CLICKHOUSE_URL names an odd database %q", c.database)
	}
	if u.User != nil {
		c.user = u.User.Username()
		c.password, _ = u.User.Password()
	}
	return c, nil
}

// Database is the database the connection uses.
func (c *ClickHouse) Database() string { return c.database }

// Exec runs a statement that returns nothing.
func (c *ClickHouse) Exec(ctx context.Context, sql string, params map[string]string) error {
	resp, err := c.do(ctx, sql, params, nil)
	if err != nil {
		return err
	}
	resp.Close()
	return nil
}

// Insert writes rows into table, one JSON object each.
func (c *ClickHouse) Insert(ctx context.Context, table string, rows []map[string]any) error {
	if len(rows) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	resp, err := c.do(ctx, "INSERT INTO "+table+" FORMAT JSONEachRow", nil, &buf)
	if err != nil {
		return err
	}
	resp.Close()
	return nil
}

// Query runs a SELECT and returns its rows. Values reach the query only as
// parameters: {name:Type} in sql, the value in params.
func (c *ClickHouse) Query(ctx context.Context, sql string, params map[string]string) ([]map[string]any, error) {
	resp, err := c.do(ctx, sql+"\nFORMAT JSON", params, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Close()
	var out struct {
		Data []map[string]any `json:"data"`
	}
	dec := json.NewDecoder(resp)
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("clickhouse: read answer: %w", err)
	}
	return out.Data, nil
}

// Ping checks the server answers.
func (c *ClickHouse) Ping(ctx context.Context) error {
	_, err := c.Query(ctx, "SELECT 1 AS ok", nil)
	return err
}

func (c *ClickHouse) do(ctx context.Context, sql string, params map[string]string, body io.Reader) (io.ReadCloser, error) {
	q := url.Values{}
	q.Set("database", c.database)
	for k, v := range params {
		q.Set("param_"+k, v)
	}
	var req *http.Request
	var err error
	if body == nil {
		req, err = http.NewRequestWithContext(ctx, "POST", c.endpoint+"?"+q.Encode(), strings.NewReader(sql))
	} else {
		q.Set("query", sql)
		req, err = http.NewRequestWithContext(ctx, "POST", c.endpoint+"?"+q.Encode(), body)
	}
	if err != nil {
		return nil, err
	}
	if c.user != "" {
		req.Header.Set("X-ClickHouse-User", c.user)
		req.Header.Set("X-ClickHouse-Key", c.password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
		resp.Body.Close()
		return nil, fmt.Errorf("clickhouse: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return resp.Body, nil
}
