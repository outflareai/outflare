package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Subscription topics for lifecycle email (product updates, a newsletter).
// A topic's key is what consents on customer records point at, so it never
// changes; the name and description are what people see on the preference
// page.

var topicKeyRE = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)

type emailTopicView struct {
	ID                  string    `json:"id"`
	Key                 string    `json:"key"`
	Name                string    `json:"name"`
	Description         string    `json:"description"`
	SubscribedByDefault bool      `json:"subscribed_by_default"`
	Visibility          string    `json:"visibility"`
	Status              string    `json:"status"`
	Templates           int       `json:"templates"`
	Sent30d             int       `json:"sent_30d"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

const emailTopicSelect = `
	SELECT t.id, t.key, t.name, t.description, t.subscribed_by_default, t.visibility, t.status,
	       (SELECT count(*) FROM email_templates et WHERE et.topic_id = t.id AND et.status = 'active')::int,
	       (SELECT count(*) FROM emails e WHERE e.workspace_id = t.workspace_id AND e.topic_id = t.id
	          AND e.created_at > now() - interval '30 days' AND NOT e.test)::int,
	       t.created_at, t.updated_at
	FROM email_topics t
	WHERE t.workspace_id = $1`

// topicRef is the topic an email or template belongs to.
type topicRef struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

type nullTopic struct{ ID, Key, Name *string }

func (n nullTopic) ref() *topicRef {
	if n.ID == nil {
		return nil
	}
	return &topicRef{ID: *n.ID, Key: *n.Key, Name: *n.Name}
}

func scanEmailTopic(row pgx.Row) (emailTopicView, error) {
	var v emailTopicView
	err := row.Scan(&v.ID, &v.Key, &v.Name, &v.Description, &v.SubscribedByDefault, &v.Visibility, &v.Status,
		&v.Templates, &v.Sent30d, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

func (s *Server) listEmailTopics(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	status := r.URL.Query().Get("status")
	if status != "" && status != "active" && status != "archived" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is active or archived")
		return
	}
	rows, err := s.pool.Query(r.Context(), emailTopicSelect+` AND ($2 = '' OR t.status = $2) ORDER BY t.name, t.key`, a.WorkspaceID, status)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (emailTopicView, error) { return scanEmailTopic(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []emailTopicView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// findEmailTopic finds a topic by id or key.
func (s *Server) findEmailTopic(w http.ResponseWriter, r *http.Request, ws, ref string) (*emailTopicView, bool) {
	v, err := scanEmailTopic(s.pool.QueryRow(r.Context(), emailTopicSelect+` AND (t.id::text = $2 OR t.key = $2)`, ws, ref))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "email topic not found")
		return nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	return &v, true
}

// topicID resolves a topic id or key to its id, for templates and sends.
// "" is no topic.
func topicID(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, ws, ref string) (*string, error) {
	if ref == "" {
		return nil, nil
	}
	var id, status string
	err := q.QueryRow(ctx, `SELECT id, status FROM email_topics WHERE workspace_id = $1 AND (id::text = $2 OR key = $2)`, ws, ref).Scan(&id, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errInvalid("topic not found: " + truncate(ref, 63))
	}
	if err != nil {
		return nil, err
	}
	if status != "active" {
		return nil, errInvalid("topic " + truncate(ref, 63) + " is archived")
	}
	return &id, nil
}

func (s *Server) getEmailTopic(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findEmailTopic(w, r, a.WorkspaceID, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

type topicFields struct {
	Key                 *string `json:"key"`
	Name                *string `json:"name"`
	Description         *string `json:"description"`
	SubscribedByDefault *bool   `json:"subscribed_by_default"`
	Visibility          *string `json:"visibility"`
	Status              *string `json:"status"`
}

func (f *topicFields) validate() string {
	switch {
	case f.Key != nil && !topicKeyRE.MatchString(*f.Key):
		return "key must be 1 to 63 lowercase letters, digits or underscores, such as product_updates"
	case f.Name != nil && (strings.TrimSpace(*f.Name) == "" || len(*f.Name) > 100):
		return "name must be 1 to 100 characters"
	case f.Description != nil && len(*f.Description) > 500:
		return "description must be at most 500 characters"
	case f.Visibility != nil && *f.Visibility != "public" && *f.Visibility != "private":
		return "visibility must be public or private"
	case f.Status != nil && *f.Status != "active" && *f.Status != "archived":
		return "status must be active or archived"
	}
	return ""
}

func (s *Server) createEmailTopic(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f topicFields
	if !decode(w, r, &f) {
		return
	}
	if f.Name == nil || strings.TrimSpace(*f.Name) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	if f.Key == nil {
		k := strings.ReplaceAll(slugFrom(*f.Name), "-", "_")
		if len(k) > 63 {
			k = strings.Trim(k[:63], "_")
		}
		if k == "" {
			k = "topic"
		}
		f.Key = &k
	}
	if f.Status != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a new topic is active; archive it with PATCH")
		return
	}
	if msg := f.validate(); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	visibility := "public"
	if f.Visibility != nil {
		visibility = *f.Visibility
	}
	byDefault := boolOr(f.SubscribedByDefault, true)
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM email_topics WHERE workspace_id = $1`, a.WorkspaceID).Scan(&n); err != nil {
			return err
		}
		if n >= 100 {
			return errInvalid("a workspace can have at most 100 topics")
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO email_topics (workspace_id, key, name, description, subscribed_by_default, visibility)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (workspace_id, key) DO NOTHING RETURNING id`,
			a.WorkspaceID, *f.Key, strings.TrimSpace(*f.Name), strings.TrimSpace(deref(f.Description)), byDefault, visibility).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errConflict
		}
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "email_topic.created", "email_topic", id,
			map[string]any{"subscribed_by_default": byDefault, "visibility": visibility})
	})
	var inv errInvalid
	switch {
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
		return
	case errors.Is(err, errConflict):
		writeError(w, r, http.StatusConflict, "conflict", "another topic already has the key "+*f.Key)
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findEmailTopic(w, r, a.WorkspaceID, id); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}

// updateEmailTopic renames, describes, archives or restores a topic. The key
// is fixed: consents on customer records point at it.
func (s *Server) updateEmailTopic(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findEmailTopic(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var f topicFields
	if !decode(w, r, &f) {
		return
	}
	if f.Key != nil && *f.Key != cur.Key {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "key can't change, because consents point at it; change the name instead")
		return
	}
	if msg := f.validate(); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	var desc *string
	if f.Description != nil {
		d := strings.TrimSpace(*f.Description)
		desc = &d
	}
	var name *string
	if f.Name != nil {
		n := strings.TrimSpace(*f.Name)
		name = &n
	}
	visibility := cur.Visibility
	if f.Visibility != nil {
		visibility = *f.Visibility
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE email_topics SET name = COALESCE($3, name), description = COALESCE($4, description),
				subscribed_by_default = COALESCE($5, subscribed_by_default), visibility = COALESCE($6, visibility),
				status = COALESCE($7, status), updated_at = now()
			WHERE workspace_id = $1 AND id = $2`,
			a.WorkspaceID, cur.ID, name, desc, f.SubscribedByDefault, f.Visibility, f.Status); err != nil {
			return err
		}
		action := "email_topic.updated"
		if f.Status != nil && *f.Status != cur.Status {
			action = map[string]string{"archived": "email_topic.archived", "active": "email_topic.restored"}[*f.Status]
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "email_topic", cur.ID, map[string]any{
			"subscribed_by_default": boolOr(f.SubscribedByDefault, cur.SubscribedByDefault),
			"visibility":            visibility,
		})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findEmailTopic(w, r, a.WorkspaceID, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}
