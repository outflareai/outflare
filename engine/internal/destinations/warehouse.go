package destinations

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Warehouses: BigQuery and Snowflake take a brand's events as rows of one
// table, a batch at a time. Both run on the workspace's own account: a
// Google service account key, or a Snowflake user's key pair, sealed with
// the destination. Outflare makes the table the first time it sends.
//
// The table has one row per event:
//
//	id, name, raw_name, kind, brand_id, occurred_at, received_at,
//	user_id, anonymous_id, record_id, properties (JSON), traits (JSON)
//
// A row is written once: BigQuery drops a repeat by its insert id, and
// Snowflake's statement only adds ids the table doesn't hold.

const (
	googleToken = "https://oauth2.googleapis.com/token"
	bigQueryAPI = "https://bigquery.googleapis.com/bigquery/v2"
	// Write access to tables and their rows, nothing else.
	bigQueryScope = "https://www.googleapis.com/auth/bigquery"
)

var (
	gcpProject   = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	bqName       = regexp.MustCompile(`^[A-Za-z0-9_]{1,200}$`)
	sfAccount    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,99}(\.[A-Za-z0-9][A-Za-z0-9_-]{0,40}){0,3}$`)
	sfIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]{0,254}$`)
)

// checkWarehouse is Check for the warehouse kinds.
func checkWarehouse(kind string, c Config) error {
	switch kind {
	case "bigquery":
		if !gcpProject.MatchString(c.Project) {
			return errors.New("project must be the Google Cloud project's id, such as acme-analytics")
		}
		if !bqName.MatchString(c.Dataset) || !bqName.MatchString(c.Table) {
			return errors.New("dataset and table are required: letters, numbers and underscores")
		}
	case "snowflake":
		if !sfAccount.MatchString(c.Account) {
			return errors.New("account must be the Snowflake account identifier, such as acme-prod1, without .snowflakecomputing.com")
		}
		for name, v := range map[string]string{"user": c.User, "database": c.Database, "schema": c.Schema, "table": c.Table, "warehouse": c.Warehouse} {
			if !sfIdentifier.MatchString(v) {
				return fmt.Errorf("%s is required: a Snowflake name of letters, numbers and underscores", name)
			}
		}
		if c.Role != "" && !sfIdentifier.MatchString(c.Role) {
			return errors.New("role must be a Snowflake name of letters, numbers and underscores")
		}
	}
	return nil
}

// CheckSecret reports why a warehouse's key cannot be used, or nil, so a
// bad paste is refused when it is saved and not at the first send.
func CheckSecret(kind, secret string) error {
	switch kind {
	case "bigquery":
		_, _, err := googleKey(secret)
		return err
	case "snowflake":
		_, err := rsaKey(secret)
		return err
	}
	return nil
}

// googleKey reads a service account's JSON key file.
func googleKey(secret string) (email string, key *rsa.PrivateKey, err error) {
	var f struct {
		Type        string `json:"type"`
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
	}
	if json.Unmarshal([]byte(secret), &f) != nil || f.Type != "service_account" || f.ClientEmail == "" {
		return "", nil, errors.New("secret must be the service account's JSON key file, pasted whole")
	}
	key, err = rsaKey(f.PrivateKey)
	return f.ClientEmail, key, err
}

// rsaKey reads an unencrypted RSA private key in PEM.
func rsaKey(text string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(text)))
	if block == nil {
		return nil, errors.New("secret must hold a private key in PEM: the lines from BEGIN PRIVATE KEY to END PRIVATE KEY")
	}
	if strings.Contains(block.Type, "ENCRYPTED") {
		return nil, errors.New("the private key is encrypted with a passphrase; make one without (it is sealed here)")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if r, ok := k.(*rsa.PrivateKey); ok {
			return r, nil
		}
		return nil, errors.New("the private key must be an RSA key")
	}
	if r, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return r, nil
	}
	return nil, errors.New("the private key could not be read; paste it whole")
}

// signJWT signs claims with RS256.
func signJWT(key *rsa.PrivateKey, claims map[string]any) (string, error) {
	enc := base64.RawURLEncoding
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc.EncodeToString(body)
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + enc.EncodeToString(sig), nil
}

// result is what happened to one event of a batch.
type result struct {
	o      outcome
	reason string
}

func all(n int, o outcome, reason string) []result {
	out := make([]result, n)
	for i := range out {
		out[i] = result{o, reason}
	}
	return out
}

// row is an event as a warehouse row. JSON columns are sent as text for
// BigQuery (how its streaming API takes them) and as values for Snowflake.
func row(e Event, jsonAsText bool) map[string]any {
	r := map[string]any{"id": e.ID, "name": e.Name, "raw_name": e.RawName, "kind": e.Kind, "brand_id": e.BrandID,
		"occurred_at": e.OccurredAt.UTC().Format(time.RFC3339Nano), "received_at": e.ReceivedAt.UTC().Format(time.RFC3339Nano),
		"user_id": e.UserID, "anonymous_id": e.AnonymousID, "record_id": e.RecordID}
	props, traits := any(e.Properties), any(e.Traits)
	if e.Traits == nil {
		traits = map[string]any{}
	}
	if jsonAsText {
		p, _ := json.Marshal(props)
		t, _ := json.Marshal(traits)
		props, traits = string(p), string(t)
	}
	r["properties"], r["traits"] = props, traits
	return r
}

func (s *Sender) httpClient() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// call sends JSON and returns the status and up to 1 MB of the answer;
// status 0 means no answer.
func (s *Sender) call(ctx context.Context, method, u string, header http.Header, body any) (int, []byte) {
	var rd io.Reader
	if form, ok := body.(url.Values); ok {
		rd = strings.NewReader(form.Encode())
	} else if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, nil
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if _, ok := body.(url.Values); ok {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "outflare-destinations")
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw
}

// statusOutcome sorts an HTTP answer: no answer, too many requests and
// server errors are tried again; any other refusal is final. what names
// the tool in the reason; the tool's own words are never passed on, since
// they can quote the events.
func statusOutcome(code int, what string) (outcome, string) {
	switch {
	case code >= 200 && code < 300:
		return sent, ""
	case code == 0:
		return retry, what + " could not be reached"
	case code == 429 || code >= 500:
		return retry, fmt.Sprintf("%s answered %d", what, code)
	case code == 401 || code == 403:
		return failed, fmt.Sprintf("%s refused the key (%d): check that it is still valid and may write to the table", what, code)
	case code == 404:
		return failed, what + " has no such project, dataset or table (404)"
	}
	return failed, fmt.Sprintf("%s refused it (%d)", what, code)
}

// bigQuery returns how a BigQuery destination sends a batch.
func (s *Sender) bigQuery(d destination) (func(context.Context, []Event) []result, string) {
	email, key, err := googleKey(d.secret)
	if err != nil {
		return nil, "the service account key can't be used; paste it again under Destinations"
	}
	tokenURL, api := googleToken, bigQueryAPI
	if s.GoogleTokenURL != "" {
		tokenURL = s.GoogleTokenURL
	}
	if s.BigQueryURL != "" {
		api = s.BigQueryURL
	}
	tables := fmt.Sprintf("%s/projects/%s/datasets/%s/tables", api, d.config.Project, d.config.Dataset)
	token, ensured := "", !d.fresh
	auth := func(ctx context.Context) (outcome, string) {
		if token != "" {
			return sent, ""
		}
		now := time.Now()
		// aud is always Google's own endpoint, whatever the test override.
		jwt, err := signJWT(key, map[string]any{"iss": email, "scope": bigQueryScope, "aud": googleToken, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
		if err != nil {
			return failed, "the service account key could not sign a request"
		}
		code, raw := s.call(ctx, http.MethodPost, tokenURL, nil, url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {jwt}})
		if code < 200 || code >= 300 {
			if code == 400 || code == 401 {
				return failed, "Google refused the service account key: check that the key has not been deleted or disabled"
			}
			return statusOutcome(code, "Google sign-in")
		}
		var t struct {
			AccessToken string `json:"access_token"`
		}
		if json.Unmarshal(raw, &t) != nil || t.AccessToken == "" {
			return retry, "Google sign-in gave no token"
		}
		token = t.AccessToken
		return sent, ""
	}
	header := func() http.Header { return http.Header{"Authorization": {"Bearer " + token}} }
	ensure := func(ctx context.Context) (outcome, string) {
		field := func(name, typ string) map[string]string {
			return map[string]string{"name": name, "type": typ, "mode": "NULLABLE"}
		}
		code, _ := s.call(ctx, http.MethodPost, tables, header(), map[string]any{
			"tableReference":   map[string]string{"projectId": d.config.Project, "datasetId": d.config.Dataset, "tableId": d.config.Table},
			"description":      "Events from Outflare, one row each.",
			"timePartitioning": map[string]string{"type": "DAY", "field": "occurred_at"},
			"schema": map[string]any{"fields": []map[string]string{
				field("id", "STRING"), field("name", "STRING"), field("raw_name", "STRING"), field("kind", "STRING"), field("brand_id", "STRING"),
				field("occurred_at", "TIMESTAMP"), field("received_at", "TIMESTAMP"), field("user_id", "STRING"), field("anonymous_id", "STRING"),
				field("record_id", "STRING"), field("properties", "JSON"), field("traits", "JSON")}},
		})
		if code == 409 {
			return sent, ""
		}
		if code == 404 {
			return failed, "BigQuery has no such project or dataset (404): make the dataset first, then Outflare makes the table"
		}
		return statusOutcome(code, "BigQuery")
	}
	insert := func(ctx context.Context, events []Event) (int, []byte) {
		rows := make([]map[string]any, len(events))
		for i, e := range events {
			rows[i] = map[string]any{"insertId": e.ID, "json": row(e, true)}
		}
		return s.call(ctx, http.MethodPost, tables+"/"+d.config.Table+"/insertAll", header(),
			map[string]any{"skipInvalidRows": true, "ignoreUnknownValues": true, "rows": rows})
	}
	return func(ctx context.Context, events []Event) []result {
		if o, why := auth(ctx); o != sent {
			return all(len(events), o, why)
		}
		if !ensured {
			if o, why := ensure(ctx); o != sent {
				return all(len(events), o, why)
			}
			ensured = true
		}
		code, raw := insert(ctx, events)
		if code == 404 {
			// The table was dropped since, or was only just made: make
			// sure it is there and try once more.
			if o, why := ensure(ctx); o != sent {
				return all(len(events), o, why)
			}
			if code, raw = insert(ctx, events); code == 404 {
				return all(len(events), retry, "BigQuery's table is not ready yet")
			}
		}
		if o, why := statusOutcome(code, "BigQuery"); o != sent {
			return all(len(events), o, why)
		}
		out := all(len(events), sent, "")
		var ans struct {
			InsertErrors []struct {
				Index  int `json:"index"`
				Errors []struct {
					Reason   string `json:"reason"`
					Location string `json:"location"`
				} `json:"errors"`
			} `json:"insertErrors"`
		}
		_ = json.Unmarshal(raw, &ans)
		for _, ie := range ans.InsertErrors {
			if ie.Index < 0 || ie.Index >= len(out) {
				continue
			}
			why := "BigQuery refused the row"
			if len(ie.Errors) > 0 && bqName.MatchString(ie.Errors[0].Reason) {
				why += ": " + ie.Errors[0].Reason
				if loc := ie.Errors[0].Location; bqName.MatchString(loc) {
					why += " in " + loc
				}
			}
			out[ie.Index] = result{failed, why}
		}
		return out
	}, ""
}

const sfColumns = "ID, NAME, RAW_NAME, KIND, BRAND_ID, OCCURRED_AT, RECEIVED_AT, USER_ID, ANONYMOUS_ID, RECORD_ID, PROPERTIES, TRAITS"

// snowflake returns how a Snowflake destination sends a batch: one
// statement through the SQL API, signed in with the user's key pair.
func (s *Sender) snowflake(d destination) (func(context.Context, []Event) []result, string) {
	key, err := rsaKey(d.secret)
	if err != nil {
		return nil, "the private key can't be used; paste it again under Destinations"
	}
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, "the private key can't be used; paste it again under Destinations"
	}
	print := sha256.Sum256(pub)
	// The sign-in names the account without its region, in capitals.
	account, _, _ := strings.Cut(strings.ToUpper(d.config.Account), ".")
	who := account + "." + strings.ToUpper(d.config.User)
	endpoint := "https://" + strings.ToLower(d.config.Account) + ".snowflakecomputing.com/api/v2/statements"
	if s.SnowflakeURL != "" {
		endpoint = s.SnowflakeURL + "/api/v2/statements"
	}
	table := strings.ToUpper(d.config.Table)
	run := func(ctx context.Context, statement string, binding *string) (outcome, string) {
		now := time.Now()
		jwt, err := signJWT(key, map[string]any{"iss": who + ".SHA256:" + base64.StdEncoding.EncodeToString(print[:]), "sub": who,
			"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()})
		if err != nil {
			return failed, "the private key could not sign a request"
		}
		body := map[string]any{"statement": statement, "timeout": 60, "database": strings.ToUpper(d.config.Database),
			"schema": strings.ToUpper(d.config.Schema), "warehouse": strings.ToUpper(d.config.Warehouse)}
		if d.config.Role != "" {
			body["role"] = strings.ToUpper(d.config.Role)
		}
		if binding != nil {
			body["bindings"] = map[string]any{"1": map[string]string{"type": "TEXT", "value": *binding}}
		}
		code, raw := s.call(ctx, http.MethodPost, endpoint, http.Header{"Authorization": {"Bearer " + jwt},
			"X-Snowflake-Authorization-Token-Type": {"KEYPAIR_JWT"}}, body)
		switch code {
		case 401, 403:
			return failed, "Snowflake refused the key: check the account, the user and that the user's public key matches"
		case 404:
			return failed, "Snowflake has no such account (404): check the account identifier"
		case 408:
			return retry, "Snowflake took too long"
		case 422:
			// The statement failed: Snowflake's code says why (a missing
			// database, no right to write); never its message.
			var ans struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(raw, &ans)
			why := "Snowflake could not run the statement"
			if bqName.MatchString(ans.Code) {
				why += " (code " + ans.Code + ")"
			}
			return failed, why + ": check the database, schema, warehouse and role, and that the role may create and write the table"
		}
		return statusOutcome(code, "Snowflake")
	}
	ensured := !d.fresh
	return func(ctx context.Context, events []Event) []result {
		if !ensured {
			if o, why := run(ctx, "CREATE TABLE IF NOT EXISTS "+table+" (ID STRING, NAME STRING, RAW_NAME STRING, KIND STRING, BRAND_ID STRING, "+
				"OCCURRED_AT TIMESTAMP_TZ, RECEIVED_AT TIMESTAMP_TZ, USER_ID STRING, ANONYMOUS_ID STRING, RECORD_ID STRING, PROPERTIES VARIANT, TRAITS VARIANT)", nil); o != sent {
				return all(len(events), o, why)
			}
			ensured = true
		}
		rows := make([]map[string]any, len(events))
		for i, e := range events {
			rows[i] = row(e, false)
		}
		raw, err := json.Marshal(rows)
		if err != nil {
			return all(len(events), failed, "the events could not be written as JSON")
		}
		text := string(raw)
		o, why := run(ctx, "MERGE INTO "+table+" t USING (SELECT value:id::string AS ID, value:name::string AS NAME, value:raw_name::string AS RAW_NAME, "+
			"value:kind::string AS KIND, value:brand_id::string AS BRAND_ID, value:occurred_at::timestamp_tz AS OCCURRED_AT, "+
			"value:received_at::timestamp_tz AS RECEIVED_AT, value:user_id::string AS USER_ID, value:anonymous_id::string AS ANONYMOUS_ID, "+
			"value:record_id::string AS RECORD_ID, value:properties AS PROPERTIES, value:traits AS TRAITS "+
			"FROM TABLE(FLATTEN(input => PARSE_JSON(?)))) s ON t.ID = s.ID "+
			"WHEN NOT MATCHED THEN INSERT ("+sfColumns+") VALUES (s.ID, s.NAME, s.RAW_NAME, s.KIND, s.BRAND_ID, s.OCCURRED_AT, s.RECEIVED_AT, "+
			"s.USER_ID, s.ANONYMOUS_ID, s.RECORD_ID, s.PROPERTIES, s.TRAITS)", &text)
		return all(len(events), o, why)
	}, ""
}
