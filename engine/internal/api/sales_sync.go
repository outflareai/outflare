package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/crmmail"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

func salesThreadMatches(m salesMessage, known []salesMessage) bool {
	for _, p := range known {
		if p.MessageID == "" {
			continue
		}
		id := strings.Trim(p.MessageID, "<> ")
		if strings.Trim(m.MessageID, "<> ") == id {
			return true
		}
		for _, ref := range strings.Fields(m.InReplyTo + " " + m.References) {
			if strings.Trim(ref, "<> ") == id {
				return true
			}
		}
	}
	return false
}
func salesGmailMessage(full gmailContent, raw, owner, id, thread string) (salesMessage, error) {
	parsed, err := crmmail.Parse(raw, owner)
	if err != nil {
		return salesMessage{}, err
	}
	r, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		return salesMessage{}, err
	}
	m := salesMessage{ProviderID: id, ThreadID: thread, Subject: parsed.Subject, Body: parsed.Body, MessageID: r.Header.Get("Message-ID"), InReplyTo: r.Header.Get("In-Reply-To"), References: r.Header.Get("References"), OccurredAt: parsed.OccurredAt, Direction: "incoming", CC: []string{}, To: []string{}}
	for _, field := range []string{"From", "To", "Cc"} {
		list, e := r.Header.AddressList(field)
		if e != nil && field != "Cc" {
			return m, e
		}
		for _, v := range list {
			switch field {
			case "From":
				m.From = strings.ToLower(v.Address)
			case "To":
				m.To = append(m.To, strings.ToLower(v.Address))
			case "Cc":
				m.CC = append(m.CC, strings.ToLower(v.Address))
			}
		}
	}
	if strings.EqualFold(m.From, owner) {
		m.Direction = "outgoing"
	}
	if millis, e := strconv.ParseInt(full.InternalDate, 10, 64); e == nil {
		m.OccurredAt = time.UnixMilli(millis)
	}
	return m, nil
}
func (s *Server) salesSyncJob(ctx context.Context, job jobs.Job) (runErr error) {
	var p struct {
		ID    string `json:"id"`
		Brand string `json:"brand_id"`
	}
	if json.Unmarshal(job.Payload, &p) != nil || job.WorkspaceID == nil || !validUUID(p.ID) || !validUUID(p.Brand) {
		return jobs.ErrPermanent
	}
	a := &access{WorkspaceID: *job.WorkspaceID, Brand: p.Brand}
	var owner string
	err := s.pool.QueryRow(ctx, `SELECT owner_id::text FROM sales_mail_connections WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 AND status='connected'`, a.WorkspaceID, a.Brand, p.ID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	a.UserID = owner
	// A session advisory lock prevents overlapping polls without holding a
	// database transaction open during provider requests.
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, "sales-sync:"+p.ID).Scan(&locked); err != nil || !locked {
		return err
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(c, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, "sales-sync:"+p.ID)
	}()
	defer func() {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := s.enqueueSales(c, s.pool, a, jobSalesSync, p.ID, time.Now().Add(5*time.Minute)); err != nil {
			runErr = errors.Join(runErr, err)
		}
		if runErr != nil {
			_, _ = s.pool.Exec(c, `UPDATE sales_mail_connections SET error_code='sync_failed' WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, p.ID)
		}
	}()
	id, credentials, token, err := s.salesGrant(ctx, a, owner)
	if err != nil {
		return err
	}
	if id != p.ID {
		return nil
	}
	rows, err := s.pool.Query(ctx, `SELECT l.id::text FROM sales_leads l JOIN records r ON r.id=l.record_id AND r.workspace_id=l.workspace_id AND r.brand_id=l.brand_id WHERE l.workspace_id=$1 AND l.brand_id=$2 AND (l.owner_id=$3 OR l.sdr_id=$3 OR l.ae_id=$3) AND r.deleted_at IS NULL AND r.merged_into IS NULL ORDER BY l.id`, a.WorkspaceID, a.Brand, owner)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, leadID := range ids {
		l, err := s.salesLoad(ctx, s.pool, a, leadID, false)
		if err != nil {
			return err
		}
		known, err := s.salesMessages(ctx, s.pool, a, l.ID)
		if err != nil {
			return err
		}
		if len(known) == 0 {
			continue
		}
		privacy, err := s.crmMailContext(ctx, a, owner, l.RecordID)
		if err != nil {
			return err
		}
		if len(privacy.brand.InternalDomains) == 0 {
			return errors.New("configure internal email domains in CRM mail policy before Sales capture")
		}
		since := l.CreatedAt
		for _, m := range known {
			if m.OccurredAt.Before(since) {
				since = m.OccurredAt
			}
		}
		// Always reconcile from this conversation's beginning, with pagination.
		// A reconnect or outage cannot silently skip mail older than a rolling window.
		query := fmt.Sprintf("{from:%s to:%s cc:%s} after:%d -in:spam -in:trash -in:drafts -in:scheduled ", l.Email, l.Email, l.Email, since.Add(-time.Hour).Unix()) + crmmail.ExcludeLabels(privacy.brand, privacy.personal)
		page := ""
		candidates := []salesMessage{}
		seenPages := map[string]bool{}
		for {
			var batch struct {
				Messages []struct {
					ID     string `json:"id"`
					Thread string `json:"threadId"`
				} `json:"messages"`
				Next string `json:"nextPageToken"`
			}
			if err = s.oauth.ReadPersonalGoogle(ctx, token, "messages", url.Values{"q": {query}, "maxResults": {"100"}, "pageToken": {page}}, &batch); err != nil {
				return err
			}
			for _, item := range batch.Messages {
				var full gmailContent
				if err = s.oauth.ReadPersonalGoogle(ctx, token, "messages/"+url.PathEscape(item.ID), url.Values{"format": {"full"}}, &full); err != nil {
					return err
				}
				if privateGoogleLabels(full.Labels) {
					continue
				}
				raw, e := gmailTextMessage(full.Payload)
				if e != nil {
					return errors.New("Sales message could not be decoded; catch-up is incomplete")
				}
				parsed, e := crmmail.Parse(raw, credentials.Grant.Email)
				if e != nil {
					continue
				}
				if !salesMailEligible(privacy, parsed, known) {
					continue
				}
				msg, e := salesGmailMessage(full, raw, credentials.Grant.Email, item.ID, item.Thread)
				if e != nil {
					return e
				}
				candidates = append(candidates, msg)
			}
			if batch.Next == "" {
				break
			}
			if seenPages[batch.Next] {
				return errors.New("Google repeated a page token; catch-up is incomplete")
			}
			seenPages[batch.Next] = true
			page = batch.Next
		}
		// Gmail pages newest first. Resolve the whole reference graph so a
		// reconnect imports descendants even when their parents arrived later.
		slices.SortFunc(candidates, func(a, b salesMessage) int { return a.OccurredAt.Compare(b.OccurredAt) })
		for len(candidates) > 0 {
			waiting := []salesMessage{}
			progress := false
			for _, msg := range candidates {
				if !salesThreadMatches(msg, known) {
					waiting = append(waiting, msg)
					continue
				}
				if err = s.retainSalesSyncMessage(ctx, a, p.ID, l, msg, "google:"+credentials.Grant.Subject); err != nil {
					return err
				}
				known = append(known, msg)
				progress = true
			}
			if !progress {
				break
			}
			candidates = waiting
		}
		// The original Bison mailbox remains a source after the rep is introduced.
		if l.IntegrationID != nil {
			opened, e := s.integrations.Open(ctx, s.pool, a.WorkspaceID, *l.IntegrationID)
			if e != nil {
				return e
			}
			source, ok := opened.Connector.(connectors.ReplyThreadSource)
			if !ok || opened.Connector.Definition().ID != "emailbison" || opened.BrandID != nil && *opened.BrandID != a.Brand || len(opened.BrandIDs) > 0 && !slices.Contains(opened.BrandIDs, a.Brand) {
				return errors.New("original EmailBison source is unavailable")
			}

			var ref salesSource
			var sealed []byte
			if e = s.pool.QueryRow(ctx, `SELECT source_sealed FROM sales_leads WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, l.ID).Scan(&sealed); e != nil {
				return e
			}
			if e = s.salesOpen(sealed, l.ID, &ref); e != nil {
				return e
			}
			replies, e := source.ReadReplyThread(ctx, opened.Client, opened.Creds, ref.ReplyID)
			if e != nil {
				return e
			}
			for _, reply := range replies {
				if reply.MailboxExternalID != "" && reply.MailboxExternalID != ref.SenderID {
					continue
				}
				msg := salesMessage{Direction: reply.Direction, From: reply.FromEmail, To: reply.To, CC: reply.CC, Subject: reply.Subject, Body: reply.Body, MessageID: reply.MessageID, InReplyTo: reply.InReplyTo, References: reply.References, OccurredAt: reply.ReceivedAt, ProviderID: reply.ExternalID}
				if msg.Body == "" && reply.HTML != "" {
					parsed, e := crmmail.Parse("From: capture@outflare.invalid\r\nTo: archive@outflare.invalid\r\nContent-Type: text/html; charset=utf-8\r\n\r\n"+reply.HTML, "capture@outflare.invalid")
					if e != nil {
						return e
					}
					msg.Body = parsed.Body
				}
				if msg.MessageID != "" {
					msg.MessageID = "<" + strings.Trim(msg.MessageID, "<> ") + ">"
				}
				participants := append(append([]string{msg.From}, msg.To...), msg.CC...)
				if !salesMailEligible(privacy, crmmail.Message{OwnerEmail: credentials.Grant.Email, Participants: participants}, known) {
					continue
				}

				err = s.retainSalesSyncMessage(ctx, a, p.ID, l, msg, "emailbison:"+*l.IntegrationID)
				if err != nil {
					return err
				}
			}
		}
	}
	// Reconcile interrupted/uncertain Gmail sends on each successful poll.
	pending, err := s.pool.Query(ctx, `SELECT id::text FROM sales_messages WHERE workspace_id=$1 AND brand_id=$2 AND sender_id=$3 AND kind<>'introduction' AND status IN ('sending','uncertain')`, a.WorkspaceID, a.Brand, owner)
	if err != nil {
		return err
	}
	pendingIDs, err := pgx.CollectRows(pending, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range pendingIDs {
		payload, _ := json.Marshal(map[string]string{"id": id, "brand_id": a.Brand})
		if err = s.salesSendJob(ctx, jobs.Job{WorkspaceID: &a.WorkspaceID, Payload: payload}); err != nil {
			return err
		}
	}
	_, err = s.pool.Exec(ctx, `UPDATE sales_mail_connections SET last_synced_at=now(),error_code=NULL WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, p.ID)
	return err
}

// Sales threads intentionally include the colleague being introduced. Only
// colleagues already present in this lead's recorded envelope are allowed;
// the CRM's address/domain exclusions still apply to every participant.
func salesMailEligible(c mailContext, m crmmail.Message, known []salesMessage) bool {
	allowed := map[string]bool{}
	for _, msg := range known {
		for _, address := range append(append([]string{msg.From}, msg.To...), msg.CC...) {
			allowed[strings.ToLower(address)] = true
		}
	}
	blocked := func(address string, p crmmail.Policy) bool {
		if slices.Contains(p.BlockedAddresses, address) {
			return true
		}
		d := crmmail.Domain(address)
		for _, b := range p.BlockedDomains {
			if d == b || strings.HasSuffix(d, "."+b) {
				return true
			}
		}
		return false
	}
	filtered := m
	filtered.Participants = nil
	for _, address := range m.Participants {
		if blocked(address, c.brand) || blocked(address, c.personal) {
			return false
		}
		for _, d := range crmmail.PrivateSenderDomains {
			if crmmail.Domain(address) == d || strings.HasSuffix(crmmail.Domain(address), "."+d) {
				return false
			}
		}
		// The original Bison sender can use a sending domain distinct from the
		// company domain. Keep it and the explicitly introduced colleagues in
		// this thread without broadening capture to their other conversations.
		if address != m.OwnerEmail && allowed[address] && !slices.Contains(c.emails, address) {
			continue
		}
		filtered.Participants = append(filtered.Participants, address)
	}
	return c.eligible(filtered)
}

// Consent and membership are locked only while retaining a provider result.
// Disconnect/role changes cannot race a commit that keeps private mail.
func (s *Server) retainSalesSyncMessage(ctx context.Context, a *access, connection string, l salesLead, msg salesMessage, provider string) error {
	stored := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `SELECT c.id::text FROM sales_mail_connections c JOIN memberships m ON m.workspace_id=c.workspace_id AND m.user_id=c.owner_id JOIN users u ON u.id=m.user_id JOIN sales_members sm ON sm.workspace_id=c.workspace_id AND sm.brand_id=c.brand_id AND sm.user_id=c.owner_id WHERE c.workspace_id=$1 AND c.brand_id=$2 AND c.id=$3 AND c.owner_id=$4 AND c.status='connected' AND sm.role IN ('sdr','ae') AND u.disabled_at IS NULL AND (m.brand_ids IS NULL OR cardinality(m.brand_ids)=0 OR c.brand_id=ANY(m.brand_ids)) FOR SHARE OF c,m,u,sm`, a.WorkspaceID, a.Brand, connection, a.UserID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		cur, err := s.salesLoad(ctx, tx, a, l.ID, true)
		if err != nil {
			return err
		}
		if deref(cur.OwnerID) != a.UserID && deref(cur.SDRID) != a.UserID && deref(cur.AEID) != a.UserID {
			return nil
		}
		_, fresh, err := s.storeSalesMessage(ctx, tx, a, cur, msg, provider)
		if err != nil {
			return err
		}
		if fresh {
			stored = true
			return s.salesEvent(ctx, tx, a, cur, "sync", "message_received", nil, nil)
		}
		return nil
	})
	if err == nil && stored {
		metrics.SalesMessageStored(msg.Direction)
	}
	return err
}
