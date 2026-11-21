// Package intercom copies a workspace's Intercom conversations, with what
// its AI agent Fin did in each, into the support inbox. It only reads.
package intercom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Version is the Intercom REST API version every call asks for.
const Version = "2.17"

// API hosts per data region. A workspace hosted in Europe or Australia
// must use its own region's host.
var hosts = map[string]string{
	"us": "https://api.intercom.io",
	"eu": "https://api.eu.intercom.io",
	"au": "https://api.au.intercom.io",
}

// Intercom is the connector.
type Intercom struct{}

// All returns the connectors in this package.
func All() []c.Connector { return []c.Connector{Intercom{}} }

func (Intercom) Definition() c.Definition {
	return c.Definition{
		ID: "intercom", Name: "Intercom", Category: c.CategorySupport,
		Summary: "Copy your Intercom conversations, and what Fin did in each, into the support inbox, and keep them in step.",
		Description: "Reads every conversation with its messages, notes, tags, assignee, rating and Fin's results (how it ended, its rating and the sources it used), " +
			"and adds each customer to your people. Read only: conversations stay in Intercom and are answered there; Outflare keeps a copy that updates every few minutes.",
		Website: "https://www.intercom.com", DocsURL: "https://developers.intercom.com/docs/references/rest-api/api.intercom.io/conversations/searchconversations",
		KeyURL: "https://app.intercom.com/a/apps/_/developer-hub",
		Auth:   c.AuthOAuth2, AuthMethods: c.Both,
		OAuth: &c.OAuth{
			AuthURL: "https://app.intercom.com/oauth", TokenURL: "https://api.intercom.io/auth/eagle/token",
			// Intercom sets an app's permissions on the app itself, not per
			// sign-in: give it read access to conversations, users and
			// companies, and admins.
			Scopes: []string{},
		},
		Fields: []c.Field{
			{Name: "api_key", Label: "Access token", Secret: true,
				Help: "In Intercom: Settings, Integrations, Developer Hub, New app (an internal app for your workspace), then Authentication. Copy the access token. It can read conversations, users and admins; Outflare never writes."},
			{Name: "region", Label: "Data region", Optional: true, Placeholder: "us",
				Help: "us, eu or au: where your Intercom workspace is hosted. Leave empty for the US."},
		},
		Capabilities: []string{c.CapImportSupport},
		Pricing:      "Works on every Intercom plan that has the REST API.",
	}
}

func base(cl *c.Client, creds c.Creds) (string, error) {
	region := strings.ToLower(strings.TrimSpace(creds["region"]))
	if region == "" {
		region = "us"
	}
	h, ok := hosts[region]
	if !ok {
		return "", c.Errf(c.ErrInvalid, "Intercom's data region is us, eu or au, not %q.", region)
	}
	return cl.Base("intercom", h), nil
}

// call sends one request to Intercom's API and decodes the answer into out.
func call(ctx context.Context, cl *c.Client, creds c.Creds, method, path string, body, out any) (int, error) {
	b, err := base(cl, creds)
	if err != nil {
		return 0, err
	}
	var rd *bytes.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(buf)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, b+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token(creds))
	req.Header.Set("Intercom-Version", Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.DoJSON(ctx, cl, "Intercom", req, out)
}

func (Intercom) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var me struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
		App   struct {
			IDCode string `json:"id_code"`
			Name   string `json:"name"`
			Region string `json:"region"`
		} `json:"app"`
	}
	if _, err := call(ctx, cl, creds, http.MethodGet, "/me", nil, &me); err != nil {
		return c.Account{}, err
	}
	if me.App.IDCode == "" {
		return c.Account{}, c.Errf(c.ErrInvalid, "Intercom didn't say which workspace the token is for.")
	}
	acct := c.Account{ID: me.App.IDCode, Name: me.App.Name, Email: me.Email}
	if me.App.Region != "" {
		acct.Details = map[string]string{"region": me.App.Region}
	}
	return acct, nil
}

// App returns the Intercom workspace's id (its app id), which keeps ids from
// two Intercom workspaces apart.
func App(ctx context.Context, cl *c.Client, creds c.Creds) (string, error) {
	a, err := Intercom{}.Test(ctx, cl, creds)
	return a.ID, err
}

func (Intercom) SupportAgents(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.SupportAgent, error) {
	var admins struct {
		Admins []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"admins"`
	}
	if _, err := call(ctx, cl, creds, http.MethodGet, "/admins", nil, &admins); err != nil {
		return nil, err
	}
	var teams struct {
		Teams []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"teams"`
	}
	if _, err := call(ctx, cl, creds, http.MethodGet, "/teams", nil, &teams); err != nil {
		return nil, err
	}
	out := make([]c.SupportAgent, 0, len(admins.Admins)+len(teams.Teams))
	for _, a := range admins.Admins {
		out = append(out, c.SupportAgent{Kind: "admin", ID: a.ID, Name: a.Name, Email: a.Email})
	}
	for _, t := range teams.Teams {
		out = append(out, c.SupportAgent{Kind: "team", ID: t.ID, Name: t.Name})
	}
	return out, nil
}

// maxPage is the most conversations Intercom's search returns at once.
const maxPage = 150

func (Intercom) SupportChanged(ctx context.Context, cl *c.Client, creds c.Creds, since time.Time, cursor string, limit int) ([]c.SupportRef, string, error) {
	limit = max(1, min(limit, maxPage))
	pagination := map[string]any{"per_page": limit}
	if cursor != "" {
		pagination["starting_after"] = cursor
	}
	body := map[string]any{
		"query":      map[string]any{"field": "updated_at", "operator": ">", "value": max(since.Unix(), 0)},
		"pagination": pagination,
		"sort":       map[string]any{"field": "updated_at", "order": "ascending"},
	}
	var res struct {
		Conversations []struct {
			ID        flexID `json:"id"`
			UpdatedAt int64  `json:"updated_at"`
		} `json:"conversations"`
		Pages struct {
			Next *struct {
				StartingAfter string `json:"starting_after"`
			} `json:"next"`
		} `json:"pages"`
	}
	if _, err := call(ctx, cl, creds, http.MethodPost, "/conversations/search", body, &res); err != nil {
		return nil, "", err
	}
	refs := make([]c.SupportRef, 0, len(res.Conversations))
	for _, cv := range res.Conversations {
		refs = append(refs, c.SupportRef{ID: string(cv.ID), UpdatedAt: unix(cv.UpdatedAt)})
	}
	next := ""
	if res.Pages.Next != nil && len(res.Conversations) > 0 {
		next = res.Pages.Next.StartingAfter
	}
	return refs, next, nil
}

func (Intercom) SupportConversation(ctx context.Context, cl *c.Client, creds c.Creds, id string) (c.SupportConversation, error) {
	if id == "" || strings.ContainsAny(id, "/?#") {
		return c.SupportConversation{}, c.Errf(c.ErrInvalid, "not an Intercom conversation id")
	}
	var cv conversation
	if _, err := call(ctx, cl, creds, http.MethodGet, "/conversations/"+url.PathEscape(id)+"?display_as=plaintext", nil, &cv); err != nil {
		return c.SupportConversation{}, err
	}
	return cv.convert(), nil
}

func (Intercom) SupportContact(ctx context.Context, cl *c.Client, creds c.Creds, id string) (c.SupportContact, bool, error) {
	if id == "" || strings.ContainsAny(id, "/?#") {
		return c.SupportContact{}, false, nil
	}
	var ct struct {
		ID    string `json:"id"`
		Role  string `json:"role"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	status, err := call(ctx, cl, creds, http.MethodGet, "/contacts/"+url.PathEscape(id), nil, &ct)
	var ce *c.Error
	if (status == http.StatusNotFound || status == http.StatusGone) && errors.As(err, &ce) {
		return c.SupportContact{}, false, nil // deleted, or merged into another contact
	}
	if err != nil {
		return c.SupportContact{}, false, err
	}
	return c.SupportContact{ID: ct.ID, Email: strings.ToLower(strings.TrimSpace(ct.Email)), Name: strings.TrimSpace(ct.Name), Role: ct.Role}, true, nil
}

// flexID reads an id Intercom sends as a string or a number.
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "null" {
		s = ""
	}
	*f = flexID(s)
	return nil
}

// assignee reads an assignee id: a string, a number, or 0 or null for none.
func assignee(f flexID) string {
	if f == "" || f == "0" {
		return ""
	}
	return string(f)
}

func unix(s int64) time.Time {
	if s <= 0 {
		return time.Time{}
	}
	return time.Unix(s, 0).UTC()
}

func unixPtr(s *int64) *time.Time {
	if s == nil || *s <= 0 {
		return nil
	}
	t := unix(*s)
	return &t
}

type author struct {
	Type        string `json:"type"`
	ID          flexID `json:"id"`
	Name        string `json:"name"`
	FromAIAgent bool   `json:"from_ai_agent"`
	IsAIAnswer  bool   `json:"is_ai_answer"`
}

type attachment struct {
	Name string `json:"name"`
}

type part struct {
	ID          flexID       `json:"id"`
	PartType    string       `json:"part_type"`
	Body        *string      `json:"body"`
	CreatedAt   int64        `json:"created_at"`
	Author      author       `json:"author"`
	Attachments []attachment `json:"attachments"`
	Redacted    bool         `json:"redacted"`
}

type conversation struct {
	ID        flexID `json:"id"`
	Title     string `json:"title"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	State     string `json:"state"`
	Priority  string `json:"priority"`
	Admin     flexID `json:"admin_assignee_id"`
	Team      flexID `json:"team_assignee_id"`
	Source    struct {
		ID          flexID       `json:"id"`
		Type        string       `json:"type"`
		DeliveredAs string       `json:"delivered_as"`
		Subject     string       `json:"subject"`
		Body        string       `json:"body"`
		Author      author       `json:"author"`
		Attachments []attachment `json:"attachments"`
		Redacted    bool         `json:"redacted"`
	} `json:"source"`
	Contacts struct {
		Contacts []struct {
			ID flexID `json:"id"`
		} `json:"contacts"`
	} `json:"contacts"`
	Tags struct {
		Tags []struct {
			Name string `json:"name"`
		} `json:"tags"`
	} `json:"tags"`
	Statistics *struct {
		FirstAdminReplyAt *int64 `json:"first_admin_reply_at"`
		LastCloseAt       *int64 `json:"last_close_at"`
		CountReopens      int    `json:"count_reopens"`
	} `json:"statistics"`
	Rating *struct {
		Rating    *int   `json:"rating"`
		Remark    string `json:"remark"`
		CreatedAt int64  `json:"created_at"`
	} `json:"conversation_rating"`
	AIParticipated bool `json:"ai_agent_participated"`
	AI             *struct {
		SourceType     string `json:"source_type"`
		SourceTitle    string `json:"source_title"`
		LastAnswerType string `json:"last_answer_type"`
		Resolution     string `json:"resolution_state"`
		Rating         *int   `json:"rating"`
		RatingRemark   string `json:"rating_remark"`
		ContentSources struct {
			ContentSources []struct {
				ContentType string `json:"content_type"`
				Title       string `json:"title"`
				URL         string `json:"url"`
				Locale      string `json:"locale"`
			} `json:"content_sources"`
		} `json:"content_sources"`
	} `json:"ai_agent"`
	Parts struct {
		Parts []part `json:"conversation_parts"`
	} `json:"conversation_parts"`
}

// priorities maps Intercom's priority (2.17's levels, or the older
// priority / not_priority) to the inbox's.
var priorities = map[string]string{
	"low": "low", "medium": "medium", "high": "high", "urgent": "urgent", "priority": "high",
}

func (cv conversation) convert() c.SupportConversation {
	out := c.SupportConversation{
		ID: string(cv.ID), Channel: "chat", ExternalChannel: cv.Source.Type,
		Subject: strings.TrimSpace(cv.Title), Priority: priorities[cv.Priority],
		CreatedAt: unix(cv.CreatedAt), UpdatedAt: unix(cv.UpdatedAt),
		AssigneeID: assignee(cv.Admin), TeamID: assignee(cv.Team), Tags: []string{},
	}
	if cv.Source.Type == "email" {
		out.Channel = "email"
	}
	if out.Subject == "" {
		out.Subject = strings.TrimSpace(cv.Source.Subject)
	}
	switch cv.State {
	case "closed":
		out.Status = "closed"
	case "snoozed":
		out.Status = "pending"
	default:
		out.Status = "open"
	}
	if len(cv.Contacts.Contacts) > 0 {
		out.ContactID = string(cv.Contacts.Contacts[0].ID)
	}
	for _, t := range cv.Tags.Tags {
		if t.Name != "" {
			out.Tags = append(out.Tags, t.Name)
		}
	}
	if st := cv.Statistics; st != nil {
		out.FirstReplyAt = unixPtr(st.FirstAdminReplyAt)
		out.Reopens = max(st.CountReopens, 0)
		if out.Status == "closed" {
			out.ClosedAt = unixPtr(st.LastCloseAt)
		}
	}
	if r := cv.Rating; r != nil && r.Rating != nil && *r.Rating >= 1 && *r.Rating <= 5 {
		out.Rating = &c.SupportRating{Score: *r.Rating, Remark: r.Remark, At: unix(r.CreatedAt)}
	}
	if a := cv.AI; a != nil && (cv.AIParticipated || a.Resolution != "" || a.LastAnswerType != "") {
		ai := &c.SupportAI{Agent: "fin", Resolution: a.Resolution, AnswerType: a.LastAnswerType, SourceType: a.SourceType,
			SourceTitle: a.SourceTitle, RatingRemark: a.RatingRemark, Sources: []c.SupportAISource{}}
		if a.Rating != nil && *a.Rating >= 1 && *a.Rating <= 5 {
			ai.Rating = *a.Rating
		}
		for _, s := range a.ContentSources.ContentSources {
			ai.Sources = append(ai.Sources, c.SupportAISource{Type: s.ContentType, Title: s.Title, URL: s.URL, Locale: s.Locale})
		}
		out.AI = ai
	}
	// The message that started it.
	src := cv.Source
	if body := text(src.Body, len(src.Attachments), src.Redacted); body != "" {
		m := message(src.Author, "", body, out.CreatedAt)
		m.ID = "source:" + string(cv.ID)
		if src.ID != "" {
			m.ID = "source:" + string(src.ID)
		}
		out.Messages = append(out.Messages, m)
	}
	for _, p := range cv.Parts.Parts {
		body := ""
		if p.Body != nil {
			body = *p.Body
		}
		body = text(body, len(p.Attachments), p.Redacted)
		if body == "" || p.ID == "" {
			continue // assignments, state changes and the like with nothing said
		}
		m := message(p.Author, p.PartType, body, unix(p.CreatedAt))
		m.ID = string(p.ID)
		out.Messages = append(out.Messages, m)
	}
	// Older parts don't flag Fin's answers: a bot called Fin in a
	// conversation Fin took part in is Fin.
	if out.AI != nil {
		for i, m := range out.Messages {
			if m.AuthorKind == "bot" && m.AuthorType == c.SupportFromSystem && strings.HasPrefix(strings.ToLower(m.AuthorName), "fin") {
				out.Messages[i].AuthorType = c.SupportFromAI
			}
		}
	}
	return out
}

// text is a message's plain text, saying so when it only had attachments
// or Intercom removed its text.
func text(body string, attachments int, redacted bool) string {
	body = strings.TrimSpace(body)
	switch {
	case body != "":
		return body
	case redacted:
		return "(removed in Intercom)"
	case attachments == 1:
		return "(1 attachment)"
	case attachments > 1:
		return "(" + strconv.Itoa(attachments) + " attachments)"
	}
	return ""
}

// message reads who wrote a message and whether the customer saw it.
func message(a author, partType, body string, at time.Time) c.SupportMessage {
	m := c.SupportMessage{Body: body, At: at, AuthorID: string(a.ID), AuthorName: strings.TrimSpace(a.Name)}
	note := strings.HasPrefix(partType, "note")
	switch a.Type {
	case "user", "lead", "contact":
		m.Kind, m.AuthorType, m.AuthorID, m.AuthorName = "in", c.SupportFromCustomer, "", ""
		return m
	case "bot":
		// Workflows and custom bots are bots too; only Fin's answers are AI.
		m.AuthorKind, m.AuthorType = "bot", c.SupportFromSystem
	case "team":
		m.AuthorKind, m.AuthorType = "team", c.SupportFromAgent
	case "admin":
		m.AuthorKind, m.AuthorType = "admin", c.SupportFromAgent
	default:
		m.AuthorKind, m.AuthorType, m.AuthorID = "", c.SupportFromSystem, ""
	}
	if a.FromAIAgent || a.IsAIAnswer {
		m.AuthorType = c.SupportFromAI
	}
	m.Kind = "out"
	if note {
		m.Kind = "note"
	}
	return m
}
