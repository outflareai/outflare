package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/crmmail"
)

func (s *Server) routesCRMMail(mux *http.ServeMux) {
	s.routesCRMGoogle(mux)
	base := "/v1/brands/{brand}/crm/mail/"
	mux.HandleFunc("GET "+base+"policy", s.onBrand(auth.ScopeMembersManage, s.getCRMMailConfig))
	mux.HandleFunc("PUT "+base+"policy", s.onBrand(auth.ScopeMembersManage, s.putCRMMailConfig))
	for _, path := range []string{"preferences", "records/{record}/sharing"} {
		mux.HandleFunc("GET "+base+path, s.onBrand(auth.ScopeCustomersRead, s.getCRMMailConfig))
		mux.HandleFunc("PUT "+base+path, s.onBrand(auth.ScopeCustomersManage, s.putCRMMailConfig))
	}
	mux.HandleFunc("GET "+base+"records/{record}/messages", s.onBrand(auth.ScopeCustomersRead, s.listCRMMail))
	mux.HandleFunc("POST "+base+"records/{record}/messages", s.onBrand(auth.ScopeCustomersManage, s.importCRMMail))
	mux.HandleFunc("DELETE "+base+"messages", s.onBrand(auth.ScopeCustomersManage, s.clearCRMMail))
	mux.HandleFunc("DELETE "+base+"messages/{id}", s.onBrand(auth.ScopeCustomersManage, s.deleteCRMMail))
}

// Personal messages never inherit an automation token's or support session's
// access. A real person must use their own authenticated session.
func (s *Server) crmMailPerson(w http.ResponseWriter, r *http.Request) bool {
	a := accessFrom(r.Context())
	if a.UserID == "" || a.TokenID != "" || a.ViewID != "" {
		writeError(w, r, 403, "forbidden", "personal CRM mail requires your own signed-in session")
		return false
	}
	return true
}
func mailConfigResource(r *http.Request) (string, bool) {
	if strings.HasSuffix(r.Pattern, "/policy") {
		return "crm_mail_policy", false
	}
	if strings.HasSuffix(r.Pattern, "/sharing") {
		return "crm_mail_sharing", true
	}
	return "crm_mail_preferences", false
}
func (s *Server) mailRecord(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, a *access, id string) error {
	if !validUUID(id) {
		return errInvalid("record must be a record id")
	}
	var exists bool
	if err := q.QueryRow(ctx, `SELECT true FROM records WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 AND deleted_at IS NULL AND merged_into IS NULL AND archived_at IS NULL FOR SHARE`, a.WorkspaceID, a.Brand, id).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		return err
	}
	if !exists {
		return errNotFound
	}
	return nil
}
func mailConfig(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, table, ws, brand, owner, recordID string) (map[string]any, error) {
	where := "workspace_id=$1 AND brand_id=$2"
	args := []any{ws, brand}
	if table != "crm_mail_policy" {
		where += " AND owner_id=$3"
		args = append(args, owner)
	}
	if table == "crm_mail_sharing" {
		where += " AND record_id=$4"
		args = append(args, recordID)
	}
	var raw []byte
	var version int
	err := q.QueryRow(ctx, "SELECT config,version FROM "+table+" WHERE "+where, args...).Scan(&raw, &version)
	var config map[string]any
	if errors.Is(err, pgx.ErrNoRows) {
		if table == "crm_mail_sharing" {
			// With no choice made, a contact's mail is private, or shared
			// with the brand once its team sharing has started: say so.
			share := crmmail.Private()
			var brandRaw []byte
			if e := q.QueryRow(ctx, `SELECT config FROM crm_mail_policy WHERE workspace_id=$1 AND brand_id=$2`, ws, brand).Scan(&brandRaw); e != nil && !errors.Is(e, pgx.ErrNoRows) {
				return nil, e
			}
			var bp crmmail.Policy
			if len(brandRaw) > 0 && json.Unmarshal(brandRaw, &bp) == nil && bp.TeamSharingOn(time.Now()) {
				share = crmmail.TeamShared()
			}
			raw, _ = json.Marshal(share)
		} else {
			raw, _ = json.Marshal(crmmail.DefaultPolicy())
		}
		err = nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	if table == "crm_mail_preferences" {
		delete(config, "internal_domains")
		// Everyone sees whether the brand shares work mail, and from when.
		var brandRaw []byte
		err := q.QueryRow(ctx, `SELECT config FROM crm_mail_policy WHERE workspace_id=$1 AND brand_id=$2`, ws, brand).Scan(&brandRaw)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		var bp crmmail.Policy
		if len(brandRaw) > 0 {
			if err := json.Unmarshal(brandRaw, &bp); err != nil {
				return nil, err
			}
		}
		config["team_sharing"], config["team_sharing_from"] = bp.TeamSharing, bp.TeamSharingFrom
		config["capture_all_contacts"] = bp.CaptureAllContacts
	}
	config["version"] = version
	return config, nil
}
func (s *Server) getCRMMailConfig(w http.ResponseWriter, r *http.Request) {
	if !s.crmMailPerson(w, r) {
		return
	}
	a := accessFrom(r.Context())
	table, linked := mailConfigResource(r)
	if linked && s.allocError(w, r, s.mailRecord(r.Context(), s.pool, a, r.PathValue("record"))) {
		return
	}
	out, err := mailConfig(r.Context(), s.pool, table, a.WorkspaceID, a.Brand, a.UserID, r.PathValue("record"))
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, out)
}

var mailDomainShape = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?\.[a-z]{2,63}$`)

func mailRules(values []string, address bool) ([]string, error) {
	if len(values) > 100 {
		return nil, errInvalid("rules must have at most 100 entries")
	}
	out := []string{}
	for _, v := range values {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			continue
		}
		if address {
			a, err := mail.ParseAddress(v)
			if err != nil || a.Address != v {
				return nil, errInvalid("blocked_addresses must contain plain email addresses")
			}
		} else if !mailDomainShape.MatchString(v) || strings.Contains(v, "..") || strings.Contains(v, ".-") || strings.Contains(v, "-.") {
			return nil, errInvalid("domains must be plain domains without wildcards, protocols or addresses")
		}
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out, nil
}
func (s *Server) putCRMMailConfig(w http.ResponseWriter, r *http.Request) {
	if !s.crmMailPerson(w, r) {
		return
	}
	a := accessFrom(r.Context())
	table, linked := mailConfigResource(r)
	var config any
	var version int
	if linked {
		var in crmmail.Sharing
		if !decode(w, r, &in) {
			return
		}
		if !slices.Contains([]string{"private", "metadata", "full"}, in.Visibility) || !slices.Contains([]string{"selected", "brand"}, in.Audience) || len(in.ViewerIDs) > 100 {
			writeError(w, r, 400, "invalid_request", "sharing must specify private, metadata or full visibility and selected or brand audience")
			return
		}
		if in.ViewerIDs == nil {
			in.ViewerIDs = []string{}
		}
		for _, id := range in.ViewerIDs {
			if !validUUID(id) {
				writeError(w, r, 400, "invalid_request", "viewer_ids must contain member ids")
				return
			}
		}
		config, version = in, in.Version
	} else {
		var in crmmail.Policy
		if !decode(w, r, &in) {
			return
		}
		var err error
		if in.InternalDomains, err = mailRules(in.InternalDomains, false); s.allocError(w, r, err) {
			return
		}
		if in.BlockedDomains, err = mailRules(in.BlockedDomains, false); s.allocError(w, r, err) {
			return
		}
		if in.BlockedAddresses, err = mailRules(in.BlockedAddresses, true); s.allocError(w, r, err) {
			return
		}
		if table == "crm_mail_preferences" && len(in.InternalDomains) > 0 {
			writeError(w, r, 400, "invalid_request", "internal_domains are managed in the brand policy")
			return
		}
		// A person's preferences show the brand's team sharing but never set it.
		if table == "crm_mail_preferences" {
			in.TeamSharing, in.CaptureAllContacts = false, false
		}
		if in.PrivateLabels, err = mailLabels(in.PrivateLabels); s.allocError(w, r, err) {
			return
		}
		// When team sharing starts is the server's to say, never the caller's.
		in.TeamSharingFrom = nil
		config, version = in, in.Version
	}
	var out map[string]any
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "crm-rollout:"+a.Brand); err != nil {
			return err
		}
		// Serialize configuration writes, including creation, without lost updates.
		lock := table + ":" + a.Brand + ":" + a.UserID + ":" + r.PathValue("record")
		if table == "crm_mail_policy" {
			lock = table + ":" + a.Brand
		}
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lock); err != nil {
			return err
		}
		if linked {
			if err := s.mailRecord(r.Context(), tx, a, r.PathValue("record")); err != nil {
				return err
			}
			for _, id := range config.(crmmail.Sharing).ViewerIDs {
				var member bool
				if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM memberships WHERE workspace_id=$1 AND user_id=$2 AND (brand_ids IS NULL OR cardinality(brand_ids)=0 OR $3::uuid=ANY(brand_ids)))`, a.WorkspaceID, id, a.Brand).Scan(&member); err != nil {
					return err
				}
				if !member {
					return errInvalid("viewer_ids must be members who can access this brand")
				}
			}
		}
		current, err := mailConfig(r.Context(), tx, table, a.WorkspaceID, a.Brand, a.UserID, r.PathValue("record"))
		if err != nil {
			return err
		}
		if current["version"].(int) != version {
			return errConflict
		}
		// Switching team sharing on starts it after the notice period, and
		// tells every member of the brand now; switching it off is at once.
		var sharingChanged bool
		if table == "crm_mail_policy" {
			p := config.(crmmail.Policy)
			was, _ := current["team_sharing"].(bool)
			switch {
			case p.TeamSharing && !was:
				from := time.Now().UTC().Add(crmmail.TeamSharingNotice).Truncate(time.Second)
				p.TeamSharingFrom = &from
				sharingChanged = true
			case p.TeamSharing:
				if v, _ := current["team_sharing_from"].(string); v != "" {
					if at, err := time.Parse(time.RFC3339Nano, v); err == nil {
						p.TeamSharingFrom = &at
					}
				}
			case was:
				sharingChanged = true
			}
			config = p
			if sharingChanged {
				if err := s.crmTeamSharingChanged(r.Context(), tx, a, p); err != nil {
					return err
				}
			}
			if was, _ := current["capture_all_contacts"].(bool); was != p.CaptureAllContacts {
				if err := s.crmCaptureAllChanged(r.Context(), tx, a, p.CaptureAllContacts); err != nil {
					return err
				}
			}
		}
		raw, _ := json.Marshal(config)
		switch table {
		case "crm_mail_policy":
			_, err = tx.Exec(r.Context(), `INSERT INTO crm_mail_policy(workspace_id,brand_id,config)VALUES($1,$2,$3) ON CONFLICT(brand_id) DO UPDATE SET config=$3,version=crm_mail_policy.version+1,updated_at=now()`, a.WorkspaceID, a.Brand, raw)
		case "crm_mail_preferences":
			_, err = tx.Exec(r.Context(), `INSERT INTO crm_mail_preferences(workspace_id,brand_id,owner_id,config)VALUES($1,$2,$3,$4) ON CONFLICT(brand_id,owner_id) DO UPDATE SET config=$4,version=crm_mail_preferences.version+1,updated_at=now()`, a.WorkspaceID, a.Brand, a.UserID, raw)
		case "crm_mail_sharing":
			_, err = tx.Exec(r.Context(), `INSERT INTO crm_mail_sharing(workspace_id,brand_id,owner_id,record_id,config)VALUES($1,$2,$3,$4,$5) ON CONFLICT(owner_id,record_id) DO UPDATE SET config=$5,version=crm_mail_sharing.version+1,updated_at=now()`, a.WorkspaceID, a.Brand, a.UserID, r.PathValue("record"), raw)
		}
		if err != nil {
			return err
		}
		out, err = mailConfig(r.Context(), tx, table, a.WorkspaceID, a.Brand, a.UserID, r.PathValue("record"))
		if err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "crm_mail.privacy_updated", table, a.Brand, map[string]any{"brand_id": a.Brand, "resource": table})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, 409, "conflict", "privacy settings changed; reload before saving")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, out)
}

type mailContext struct {
	brand, personal          crmmail.Policy
	members, emails, domains []string
}

type mailRowsQuery interface {
	mailQuery
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (s *Server) crmMailContext(ctx context.Context, a *access, owner, rec string) (mailContext, error) {
	return s.crmMailContextWith(ctx, s.pool, a, owner, rec)
}
func (s *Server) crmMailContextWith(ctx context.Context, q mailRowsQuery, a *access, owner, rec string) (mailContext, error) {
	c := mailContext{brand: crmmail.DefaultPolicy(), personal: crmmail.DefaultPolicy()}
	// Unset policies are empty exclusions, not implicit sharing grants.
	for _, entry := range []struct {
		table string
		dst   *crmmail.Policy
	}{{"crm_mail_policy", &c.brand}, {"crm_mail_preferences", &c.personal}} {
		cfg, err := mailConfig(ctx, q, entry.table, a.WorkspaceID, a.Brand, owner, "")
		if err != nil {
			return c, err
		}
		raw, _ := json.Marshal(cfg)
		if err = json.Unmarshal(raw, entry.dst); err != nil {
			return c, err
		}
	}
	rows, err := q.Query(ctx, `SELECT lower(u.email) FROM users u JOIN memberships m ON m.user_id=u.id WHERE m.workspace_id=$1`, a.WorkspaceID)
	if err != nil {
		return c, err
	}
	c.members, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || rec == "" {
		return c, err
	}
	return c.forRecord(ctx, q, a.WorkspaceID, rec)
}

// forRecord is the same policies and members with one record's addresses
// and company domains.
func (c mailContext) forRecord(ctx context.Context, q mailRowsQuery, ws, rec string) (mailContext, error) {
	c.emails, c.domains = nil, nil
	rows, err := q.Query(ctx, `SELECT namespace,lower(value) FROM record_identities WHERE workspace_id=$1 AND record_id=$2 AND namespace IN ('email','domain') AND (valid_to IS NULL OR valid_to>now())`, ws, rec)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var namespace, value string
		if err = rows.Scan(&namespace, &value); err != nil {
			return c, err
		}
		if namespace == "email" {
			c.emails = append(c.emails, value)
		} else if crmmail.CaptureDomainAllowed(value) {
			c.domains = append(c.domains, value)
		}
	}
	return c, rows.Err()
}
func (c mailContext) eligible(m crmmail.Message) bool {
	return len(c.brand.InternalDomains) > 0 && crmmail.Eligible(m, c.brand, c.personal, c.members, c.emails, c.domains)
}
func mailAAD(id string) []byte { return []byte(id) }
func (s *Server) importCRMMail(w http.ResponseWriter, r *http.Request) {
	if !s.crmMailPerson(w, r) {
		return
	}
	a := accessFrom(r.Context())
	rec := r.PathValue("record")
	if s.box == nil {
		writeError(w, r, 503, "unavailable", "personal mail encryption is not configured")
		return
	}
	if s.allocError(w, r, s.mailRecord(r.Context(), s.pool, a, rec)) {
		return
	}
	var in struct {
		Raw string `json:"raw"`
	}
	if !decode(w, r, &in) {
		return
	}
	var email string
	if err := s.pool.QueryRow(r.Context(), `SELECT email FROM users WHERE id=$1`, a.UserID).Scan(&email); s.allocError(w, r, err) {
		return
	}
	m, err := crmmail.Parse(in.Raw, email)
	if err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	c, err := s.crmMailContext(r.Context(), a, a.UserID, rec)
	if s.allocError(w, r, err) {
		return
	}
	if len(c.brand.InternalDomains) == 0 {
		writeError(w, r, 400, "invalid_request", "set the internal company domains in CRM settings before importing personal mail; ask a brand administrator to complete the safeguards")
		return
	}
	if !c.eligible(m) {
		writeError(w, r, 400, "invalid_request", "message excluded: check internal participants, your exclusion rules, and the record's email or domain identity")
		return
	}
	id := newID()
	raw, _ := json.Marshal(m)
	sealed := s.box.Seal(raw, mailAAD(id))
	hash := sha256.Sum256([]byte(in.Raw))
	stored := false
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "crm-rollout:"+a.Brand); err != nil {
			return err
		}
		live, err := s.crmMailContextWith(r.Context(), tx, a, a.UserID, rec)
		if err != nil {
			return err
		}
		if !live.eligible(m) {
			return errInvalid("message is excluded by current privacy settings")
		}
		// Membership and record ownership are rechecked in the write transaction.
		if err := s.mailRecord(r.Context(), tx, a, rec); err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `INSERT INTO crm_mail_messages(id,workspace_id,brand_id,owner_id,record_id,content_sealed,source_hash,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(brand_id,owner_id,source_hash) DO NOTHING`, id, a.WorkspaceID, a.Brand, a.UserID, rec, sealed, hex.EncodeToString(hash[:]), m.OccurredAt)
		if err != nil {
			return err
		}
		stored = tag.RowsAffected() > 0
		if !stored {
			return nil
		}
		if err := queueMailPrivacyIfShared(r.Context(), tx, a.WorkspaceID, a.Brand); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "crm_mail.imported", "crm_mail_message", id, map[string]any{"brand_id": a.Brand, "record_id": rec})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"stored": stored})
}
func (s *Server) listCRMMail(w http.ResponseWriter, r *http.Request) {
	if !s.crmMailPerson(w, r) {
		return
	}
	a := accessFrom(r.Context())
	rec := r.PathValue("record")
	if s.allocError(w, r, s.mailRecord(r.Context(), s.pool, a, rec)) {
		return
	}
	if s.box == nil {
		writeError(w, r, 503, "unavailable", "personal mail encryption is not configured")
		return
	}
	limit, err := limitParam(r, 50, 100)
	if s.allocError(w, r, err) {
		return
	}
	before, ok := pageCursor(w, r, pageBefore)
	if !ok {
		return
	}
	if before != "" && !validUUID(before) {
		writeError(w, r, 400, "invalid_request", "before must be a message id")
		return
	}
	// Filter every candidate before exposing it, including to workspace admins.
	// Scan bounded batches until the visible page is full; cursors contain only
	// the last visible id, so hidden rows produce no paging/count side channel.
	out := []map[string]any{}
	var next *string
	contexts := map[string]mailContext{}
	shares := map[string]crmmail.Sharing{}
	// With the brand's team sharing on, a contact whose owner made no
	// choice is shared with the brand; one made private stays private.
	var brandPolicy crmmail.Policy
	if cfg, err := mailConfig(r.Context(), s.pool, "crm_mail_policy", a.WorkspaceID, a.Brand, "", ""); s.allocError(w, r, err) {
		return
	} else {
		raw, _ := json.Marshal(cfg)
		if err := json.Unmarshal(raw, &brandPolicy); s.allocError(w, r, err) {
			return
		}
	}
	teamShared := brandPolicy.TeamSharingOn(time.Now())
	teamDefault := map[string]bool{}
	for {
		rows, err := s.pool.Query(r.Context(), `SELECT id::text,owner_id::text,content_sealed,occurred_at,privacy FROM crm_mail_messages WHERE workspace_id=$1 AND brand_id=$2 AND record_id=$3 AND ($4='' OR id<NULLIF($4,'')::uuid) AND EXISTS(SELECT 1 FROM memberships m JOIN users u ON u.id=m.user_id AND u.disabled_at IS NULL WHERE m.workspace_id=$1 AND m.user_id=crm_mail_messages.owner_id AND (m.brand_ids IS NULL OR cardinality(m.brand_ids)=0 OR $2::uuid=ANY(m.brand_ids))) ORDER BY id DESC LIMIT 200`, a.WorkspaceID, a.Brand, rec, before)
		if s.allocError(w, r, err) {
			return
		}
		type candidate struct {
			id, owner, privacy string
			sealed             []byte
			at                 time.Time
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (candidate, error) {
			var v candidate
			err := row.Scan(&v.id, &v.owner, &v.sealed, &v.at, &v.privacy)
			return v, err
		})
		if s.allocError(w, r, err) {
			return
		}
		for _, v := range list {
			before = v.id
			share, ok := shares[v.owner]
			if !ok {
				cfg, err := mailConfig(r.Context(), s.pool, "crm_mail_sharing", a.WorkspaceID, a.Brand, v.owner, rec)
				if s.allocError(w, r, err) {
					return
				}
				raw, _ := json.Marshal(cfg)
				if err = json.Unmarshal(raw, &share); s.allocError(w, r, err) {
					return
				}
				if chosen, _ := cfg["version"].(int); chosen == 0 && teamShared {
					share = crmmail.TeamShared()
					teamDefault[v.owner] = true
				}
				shares[v.owner] = share
			}
			visibility := crmmail.Visibility(v.owner, a.UserID, share)
			if visibility == "private" {
				continue
			}
			// Team sharing shows a colleague only email the AI check found to
			// be about work; the owner's own choice to share isn't second-guessed.
			if teamDefault[v.owner] && v.owner != a.UserID && v.privacy != "work" {
				continue
			}
			c, ok := contexts[v.owner]
			if !ok {
				c, err = s.crmMailContext(r.Context(), a, v.owner, rec)
				if s.allocError(w, r, err) {
					return
				}
				contexts[v.owner] = c
			}
			raw, err := s.box.Open(v.sealed, mailAAD(v.id))
			if s.allocError(w, r, err) {
				return
			}
			var m crmmail.Message
			if err = json.Unmarshal(raw, &m); s.allocError(w, r, err) {
				return
			}
			if !c.eligible(m) {
				continue
			}
			if len(out) == limit {
				last := out[len(out)-1]["id"].(string)
				next = &last
				break
			}
			item := map[string]any{"id": v.id, "owner_id": v.owner, "record_id": rec, "occurred_at": v.at, "visibility": visibility, "subject": nil, "body": nil, "participants": nil}
			if visibility == "full" {
				item["subject"], item["body"], item["participants"] = m.Subject, m.Body, m.VisibleParticipants
				if v.owner == a.UserID {
					item["participants"] = m.Participants
				}
			}
			out = append(out, item)
		}
		if next != nil || len(list) < 200 {
			break
		}
	}
	// Reading a colleague's mail is recorded: who read whose, on which
	// contact, never what.
	others := map[string]int{}
	for _, item := range out {
		if owner := item["owner_id"].(string); owner != a.UserID && item["visibility"] == "full" {
			others[owner]++
		}
	}
	if len(others) > 0 {
		err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
			for owner, n := range others {
				if err := record(r.Context(), tx, &a.principal, a.WorkspaceID, "crm_mail.viewed", "record", rec,
					map[string]any{"brand_id": a.Brand, "owner_id": owner, "messages": n}); err != nil {
					return err
				}
			}
			return nil
		})
		if s.allocError(w, r, err) {
			return
		}
	}
	writeJSON(w, 200, map[string]any{"data": out, "next_cursor": nextCursor(pageBefore, next)})
}
func (s *Server) deleteCRMMail(w http.ResponseWriter, r *http.Request) {
	if !s.crmMailPerson(w, r) {
		return
	}
	a := accessFrom(r.Context())
	if !validUUID(r.PathValue("id")) {
		writeError(w, r, 400, "invalid_request", "id must be a message id")
		return
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "crm-rollout:"+a.Brand); err != nil {
			return err
		}
		count, err := s.deleteCRMMailCopies(r.Context(), tx, a, r.PathValue("id"))
		if err != nil {
			return err
		}
		if count == 0 {
			return errNotFound
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "crm_mail.deleted", "crm_mail_message", r.PathValue("id"), map[string]any{"brand_id": a.Brand})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": true})
}

// clearCRMMail also reaches copies hidden by exclusions, without making their
// contents or IDs visible. Scope is always the signed-in owner and brand.
func (s *Server) clearCRMMail(w http.ResponseWriter, r *http.Request) {
	if !s.crmMailPerson(w, r) {
		return
	}
	a := accessFrom(r.Context())
	var deleted int64
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "crm-rollout:"+a.Brand); err != nil {
			return err
		}
		count, err := s.deleteCRMMailCopies(r.Context(), tx, a, "")
		if err != nil {
			return err
		}
		deleted = count
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "crm_mail.cleared", "crm_mail_messages", a.Brand, map[string]any{"brand_id": a.Brand, "count": deleted})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"deleted_count": deleted})
}

func (s *Server) deleteCRMMailCopies(ctx context.Context, tx pgx.Tx, a *access, id string) (int64, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO crm_mail_suppressions(workspace_id,brand_id,owner_id,source_hash) SELECT workspace_id,brand_id,owner_id,source_hash FROM crm_mail_messages WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3 AND ($4='' OR id=NULLIF($4,'')::uuid) AND source_hash LIKE 'google:%' ON CONFLICT DO NOTHING`, a.WorkspaceID, a.Brand, a.UserID, id); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM crm_mail_messages WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3 AND ($4='' OR id=NULLIF($4,'')::uuid)`, a.WorkspaceID, a.Brand, a.UserID, id)
	return tag.RowsAffected(), err
}
