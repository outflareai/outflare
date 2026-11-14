package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type salesTemplate struct {
	ID        string `json:"id"`
	VersionID string `json:"version_id"`
	Version   int    `json:"version"`
	Name      string `json:"name"`
	Body      string `json:"body"`
	Archived  bool   `json:"archived"`
}

func (s *Server) salesTemplate(ctx context.Context, q mailQuery, a *access, id string) (salesTemplate, error) {
	var t salesTemplate
	var sealed []byte
	err := q.QueryRow(ctx, `SELECT t.id::text,v.id::text,t.version,t.archived,v.content_sealed FROM sales_templates t JOIN sales_template_versions v ON v.template_id=t.id AND v.workspace_id=t.workspace_id AND v.brand_id=t.brand_id AND v.version=t.version WHERE t.workspace_id=$1 AND t.brand_id=$2 AND t.owner_id=$3 AND t.id=$4`, a.WorkspaceID, a.Brand, a.UserID, id).Scan(&t.ID, &t.VersionID, &t.Version, &t.Archived, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, errNotFound
	}
	if err != nil {
		return t, err
	}
	var content struct{ Name, Body string }
	err = s.salesOpen(sealed, t.VersionID, &content)
	t.Name, t.Body = content.Name, content.Body
	return t, err
}
func (s *Server) listSalesTemplates(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.salesPerson(w, r); !ok {
		return
	}
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `SELECT id::text FROM sales_templates WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3 AND NOT archived ORDER BY id`, a.WorkspaceID, a.Brand, a.UserID)
	if s.allocError(w, r, err) {
		return
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if s.allocError(w, r, err) {
		return
	}
	out := []salesTemplate{}
	for _, id := range ids {
		t, e := s.salesTemplate(r.Context(), s.pool, a, id)
		if s.allocError(w, r, e) {
			return
		}
		out = append(out, t)
	}
	writeJSON(w, 200, map[string]any{"data": out})
}
func (s *Server) putSalesTemplate(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.salesPerson(w, r); !ok {
		return
	}
	a := accessFrom(r.Context())
	var in salesTemplate
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 || strings.TrimSpace(in.Body) == "" || len(in.Body) > 20000 {
		writeError(w, r, 400, "invalid_request", "provide a template name and message under 20000 characters")
		return
	}
	create := r.PathValue("id") == ""
	in.ID = r.PathValue("id")
	if create {
		in.ID = newID()
	} else if !validUUID(in.ID) {
		writeError(w, r, 404, "not_found", "template not found")
		return
	}
	in.VersionID = newID()
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if create {
			in.Version = 1
			_, err := tx.Exec(r.Context(), `INSERT INTO sales_templates(id,workspace_id,brand_id,owner_id)VALUES($1,$2,$3,$4)`, in.ID, a.WorkspaceID, a.Brand, a.UserID)
			if err != nil {
				return err
			}
		} else {
			var version int
			err := tx.QueryRow(r.Context(), `SELECT version FROM sales_templates WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3 AND id=$4 FOR UPDATE`, a.WorkspaceID, a.Brand, a.UserID, in.ID).Scan(&version)
			if errors.Is(err, pgx.ErrNoRows) {
				return errNotFound
			}
			if err != nil {
				return err
			}
			if version != in.Version {
				return recordConflict{"record_changed", "Template changed; reload before saving."}
			}
			in.Version++
			_, err = tx.Exec(r.Context(), `UPDATE sales_templates SET version=$5,archived=$6 WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3 AND id=$4`, a.WorkspaceID, a.Brand, a.UserID, in.ID, in.Version, in.Archived)
			if err != nil {
				return err
			}
		}
		_, err := tx.Exec(r.Context(), `INSERT INTO sales_template_versions(id,workspace_id,brand_id,template_id,version,content_sealed)VALUES($1,$2,$3,$4,$5,$6)`, in.VersionID, a.WorkspaceID, a.Brand, in.ID, in.Version, s.salesSeal(in.VersionID, map[string]string{"Name": in.Name, "Body": in.Body}))
		if err != nil {
			return err
		}
		action := "sales.template_updated"
		if create {
			action = "sales.template_created"
		}
		if in.Archived {
			action = "sales.template_archived"
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, action, "sales_template", in.ID, map[string]any{"brand_id": a.Brand, "version": in.Version})
	})
	if !s.recordError(w, r, err) {
		status := 200
		if create {
			status = 201
		}
		writeJSON(w, status, in)
	}
}

type salesDraft struct {
	ID                string  `json:"id"`
	Revision          int     `json:"revision"`
	Body              string  `json:"body"`
	TemplateVersionID *string `json:"template_version_id"`
	ResolvedText      string  `json:"resolved_text"`
}

func (s *Server) salesDraft(ctx context.Context, q mailQuery, a *access, lead string) (*salesDraft, error) {
	d := &salesDraft{}
	var sealed []byte
	err := q.QueryRow(ctx, `SELECT id::text,revision,template_version_id::text,content_sealed FROM sales_drafts WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3 AND owner_id=$4 ORDER BY revision DESC LIMIT 1`, a.WorkspaceID, a.Brand, lead, a.UserID).Scan(&d.ID, &d.Revision, &d.TemplateVersionID, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var text struct{ Body, ResolvedText string }
	if err = s.salesOpen(sealed, d.ID, &text); err != nil {
		return nil, err
	}
	d.Body, d.ResolvedText = text.Body, text.ResolvedText
	return d, nil
}
func resolveSalesTemplate(body string, l salesLead, m salesMember) string {
	first, _, _ := strings.Cut(l.Name, " ")
	return strings.NewReplacer("{first_name}", first, "{company}", l.Company, "{my_name}", m.Name).Replace(body)
}
func (s *Server) putSalesDraft(w http.ResponseWriter, r *http.Request) {
	m, ok := s.salesPerson(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	var in struct {
		Revision   int    `json:"revision"`
		Body       string `json:"body"`
		TemplateID string `json:"template_id"`
		Discard    bool   `json:"discard_template"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Body) > 20000 {
		writeError(w, r, 400, "invalid_request", "reply is too long")
		return
	}
	var d *salesDraft
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		l, err := s.salesLoad(r.Context(), tx, a, r.PathValue("id"), true)
		if err != nil {
			return err
		}
		if deref(l.OwnerID) != a.UserID || !slices.Contains([]string{"sdr", "ae"}, m.Role) {
			return errNotFound
		}
		d, err = s.salesDraft(r.Context(), tx, a, l.ID)
		if err != nil {
			return err
		}
		if d == nil {
			d = &salesDraft{}
		}
		if d.Revision != in.Revision {
			return recordConflict{"record_changed", "Your draft changed on another device. Reload before replacing it."}
		}
		d.ID = newID()
		d.Revision++
		d.Body = in.Body
		action := "draft_saved"
		if in.Discard || strings.TrimSpace(in.Body) == "" {
			d.TemplateVersionID = nil
			d.ResolvedText = ""
		}
		if in.TemplateID != "" {
			if !validUUID(in.TemplateID) {
				return errInvalid("invalid template")
			}
			t, e := s.salesTemplate(r.Context(), tx, a, in.TemplateID)
			if e != nil {
				return e
			}
			if t.Archived {
				return errInvalid("template is archived")
			}
			d.TemplateVersionID = &t.VersionID
			d.ResolvedText = resolveSalesTemplate(t.Body, l, m)
			d.Body = d.ResolvedText
			action = "template_applied"
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO sales_drafts(id,workspace_id,brand_id,record_id,lead_id,owner_id,revision,template_version_id,content_sealed)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, d.ID, a.WorkspaceID, a.Brand, l.RecordID, l.ID, a.UserID, d.Revision, d.TemplateVersionID, s.salesSeal(d.ID, map[string]string{"Body": d.Body, "ResolvedText": d.ResolvedText}))
		if err != nil {
			return err
		}
		return s.salesEvent(r.Context(), tx, a, l, m.Role, action, nil, map[string]any{"draft_id": d.ID, "revision": d.Revision, "template_version_id": d.TemplateVersionID})
	})
	if !s.recordError(w, r, err) {
		writeJSON(w, 200, d)
	}
}

type salesMessage struct {
	ID         string    `json:"id,omitempty"`
	Direction  string    `json:"direction"`
	Kind       string    `json:"kind,omitempty"`
	Status     string    `json:"status,omitempty"`
	From       string    `json:"from"`
	To         []string  `json:"to"`
	CC         []string  `json:"cc"`
	Subject    string    `json:"subject"`
	Body       string    `json:"body"`
	MessageID  string    `json:"message_id"`
	InReplyTo  string    `json:"in_reply_to"`
	References string    `json:"references"`
	OccurredAt time.Time `json:"occurred_at"`
	DraftID    *string   `json:"draft_id"`
	ProviderID string    `json:"provider_id"`
	ThreadID   string    `json:"thread_id"`
}

func (s *Server) salesMessages(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, a *access, lead string) ([]salesMessage, error) {
	rows, err := q.Query(ctx, `SELECT id::text,direction,kind,status,content_sealed,occurred_at,draft_id::text FROM sales_messages WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3 AND superseded_by IS NULL ORDER BY occurred_at,id`, a.WorkspaceID, a.Brand, lead)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (salesMessage, error) {
		var m salesMessage
		var sealed []byte
		var id, direction, kind, status string
		var at time.Time
		var draft *string
		e := row.Scan(&id, &direction, &kind, &status, &sealed, &at, &draft)
		if e != nil {
			return m, e
		}
		e = s.salesOpen(sealed, id, &m)
		m.ID, m.Direction, m.Kind, m.Status, m.OccurredAt, m.DraftID = id, direction, kind, status, at, draft
		return m, e
	})
	if out == nil {
		out = []salesMessage{}
	}
	return out, err
}
func validSalesMessage(m salesMessage) bool {
	if len(m.Body) > 200000 || len(m.Subject) > 1000 || len(m.To)+len(m.CC) > 100 || m.OccurredAt.IsZero() || !slices.Contains([]string{"incoming", "outgoing"}, m.Direction) {
		return false
	}
	for _, v := range append(append([]string{m.From}, m.To...), m.CC...) {
		p, e := mail.ParseAddress(v)
		if e != nil || p.Address != v || strings.ContainsAny(v, "\r\n") {
			return false
		}
	}
	for _, v := range []string{m.Subject, m.MessageID, m.InReplyTo, m.References} {
		if strings.ContainsAny(v, "\r\n") {
			return false
		}
	}
	return m.From != "" && len(m.To) > 0
}
func (s *Server) storeSalesMessage(ctx context.Context, tx pgx.Tx, a *access, l salesLead, m salesMessage, provider string) (string, bool, error) {
	if !validSalesMessage(m) || m.ProviderID == "" {
		return "", false, errInvalid("messages need provider identity, valid envelope and timestamp")
	}
	// Serialize receipts and synchronization for this lead. The network call
	// happens outside this short transaction.
	if _, err := s.salesLoad(ctx, tx, a, l.ID, true); err != nil {
		return "", false, err
	}
	identity := provider + ":" + m.ProviderID
	if m.MessageID != "" {
		identity = "rfc:" + strings.Trim(m.MessageID, "<> ")
	}
	id := newID()
	status := "received"
	if m.Direction == "outgoing" {
		status = "accepted"
	}
	tag, err := tx.Exec(ctx, `INSERT INTO sales_messages(id,workspace_id,brand_id,record_id,lead_id,direction,status,content_sealed,identity_hash,occurred_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)ON CONFLICT(workspace_id,brand_id,lead_id,identity_hash)DO NOTHING`, id, a.WorkspaceID, a.Brand, l.RecordID, l.ID, m.Direction, status, s.salesSeal(id, m), salesHash(identity), m.OccurredAt)
	if err != nil {
		return "", false, err
	}
	fresh := tag.RowsAffected() > 0
	if !fresh {
		err = tx.QueryRow(ctx, `SELECT id::text FROM sales_messages WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3 AND identity_hash=$4`, a.WorkspaceID, a.Brand, l.ID, salesHash(identity)).Scan(&id)
		if err != nil {
			return "", false, err
		}
	}
	copyID := newID()
	_, err = tx.Exec(ctx, `INSERT INTO sales_message_copies(id,workspace_id,brand_id,message_id,provider_hash,content_sealed)VALUES($1,$2,$3,$4,$5,$6)ON CONFLICT(workspace_id,brand_id,provider_hash)DO NOTHING`, copyID, a.WorkspaceID, a.Brand, id, salesHash(provider+":"+m.ProviderID), s.salesSeal(copyID, map[string]string{"provider": provider, "message_id": m.ProviderID, "thread_id": m.ThreadID, "observed_by": a.UserID}))
	if err != nil {
		return "", false, err
	}
	if fresh {
		_, err = tx.Exec(ctx, `UPDATE sales_leads SET updated_at=now(),version=version+1,reply_state=COALESCE((SELECT CASE WHEN direction='incoming' THEN 'reply_received' ELSE 'waiting_for_reply' END FROM sales_messages WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3 AND status IN ('received','accepted') AND superseded_by IS NULL ORDER BY occurred_at DESC,id DESC LIMIT 1),reply_state) WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, l.ID)
	}
	return id, fresh, err
}

type salesSource struct {
	SenderID    string          `json:"sender_id"`
	ReplyID     string          `json:"reply_id"`
	Attribution json.RawMessage `json:"attribution,omitempty"`
}

func (s *Server) importSalesLead(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Source        string          `json:"source"`
		SourceID      string          `json:"source_id"`
		RecordID      string          `json:"record_id"`
		IntegrationID string          `json:"integration_id"`
		SenderID      string          `json:"sender_id"`
		ReplyID       string          `json:"reply_id"`
		Messages      []salesMessage  `json:"messages"`
		Attribution   json.RawMessage `json:"attribution"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Source == "" || len(in.Source) > 100 || in.SourceID == "" || len(in.SourceID) > 200 || !validUUID(in.RecordID) || !validUUID(in.IntegrationID) || in.SenderID == "" || in.ReplyID == "" || len(in.Messages) == 0 || len(in.Messages) > 100 || len(in.Attribution) > 50000 {
		writeError(w, r, 400, "invalid_request", "source, CRM record, original EmailBison references and messages are required")
		return
	}
	var l salesLead
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		rec, err := lockRecord(r.Context(), tx, a, in.RecordID)
		if err != nil {
			return err
		}
		if rec.Type != "person" {
			return errInvalid("Sales lead must reference a person CRM record")
		}
		i, err := s.integrations.Get(r.Context(), tx, a.WorkspaceID, in.IntegrationID)
		if err != nil {
			return err
		}
		if i.Connector != "emailbison" || i.BrandID != nil && *i.BrandID != a.Brand || len(i.BrandIDs) > 0 && !slices.Contains(i.BrandIDs, a.Brand) {
			return errInvalid("choose this brand's original EmailBison connection")
		}
		id := newID()
		tag, err := tx.Exec(r.Context(), `INSERT INTO sales_leads(id,workspace_id,brand_id,record_id,source,source_id,integration_id,source_sealed)VALUES($1,$2,$3,$4,$5,$6,$7,$8)ON CONFLICT(workspace_id,brand_id,source,source_id)DO NOTHING`, id, a.WorkspaceID, a.Brand, in.RecordID, in.Source, in.SourceID, in.IntegrationID, s.salesSeal(id, salesSource{SenderID: in.SenderID, ReplyID: in.ReplyID, Attribution: in.Attribution}))
		if err != nil {
			return err
		}
		fresh := tag.RowsAffected() > 0
		if err = tx.QueryRow(r.Context(), `SELECT id::text FROM sales_leads WHERE workspace_id=$1 AND brand_id=$2 AND source=$3 AND source_id=$4 FOR UPDATE`, a.WorkspaceID, a.Brand, in.Source, in.SourceID).Scan(&id); err != nil {
			return err
		}
		l, err = s.salesLoad(r.Context(), tx, a, id, false)
		if err != nil {
			return err
		}
		if l.RecordID != in.RecordID || deref(l.IntegrationID) != in.IntegrationID {
			return errInvalid("source lead already maps to another record or connection")
		}
		for _, msg := range in.Messages {
			_, added, e := s.storeSalesMessage(r.Context(), tx, a, l, msg, "emailbison:"+in.IntegrationID)
			if e != nil {
				return e
			}
			if added {
				if e = s.salesEvent(r.Context(), tx, a, l, "bridge", "message_received", nil, nil); e != nil {
					return e
				}
			}
		}
		if fresh {
			if err = s.salesEvent(r.Context(), tx, a, l, "bridge", "lead_arrived", nil, map[string]string{"source": in.Source}); err != nil {
				return err
			}
		}
		l, err = s.salesLoad(r.Context(), tx, a, id, false)
		return err
	})
	if !s.recordError(w, r, err) {
		writeJSON(w, 201, l)
	}
}
