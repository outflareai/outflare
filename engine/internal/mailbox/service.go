package mailbox

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/secrets"
)

// Job kinds.
const (
	JobVerify      = "mailbox.verify"
	JobDomainCheck = "domain.check"
)

// DefaultDailyLimit is the daily send cap for a new mailbox. Reseller inboxes
// are usually run at 20 to 50 a day each; 30 is a safe start.
const DefaultDailyLimit = 30

// Service stores mailboxes and runs their background checks.
type Service struct {
	Pool     *pgxpool.Pool
	Box      *secrets.Box
	Dialer   *Dialer
	Resolver Resolver
	Log      *slog.Logger
	// Tokens signs OAuth mailboxes in. Nil means OAuth is unavailable.
	Tokens TokenSource
}

// TokenRequest identifies the mailbox an access token is for.
type TokenRequest struct {
	MailboxID, WorkspaceID, Email, Provider string
	ConnectionID                            *string
	RefreshToken                            string
}

// TokenSource returns an OAuth access token for a mailbox, and a new
// refresh token when the provider rotated it.
type TokenSource interface {
	Token(ctx context.Context, req TokenRequest) (access, rotated string, err error)
}

// Register adds the mailbox job handlers to a queue.
func (s *Service) Register(q *jobs.Queue) {
	q.Handle(JobVerify, s.verifyJob)
	q.Handle(JobDomainCheck, s.domainJob)
	q.Handle(JobServerCheck, s.serverJob)
}

// stored is the encrypted credentials blob.
type stored struct {
	SMTPPassword string `json:"smtp_password,omitempty"`
	IMAPPassword string `json:"imap_password,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// ImportResult summarises an import.
type ImportResult struct {
	Created int        `json:"created"`
	Updated int        `json:"updated"`
	Errors  []RowError `json:"errors"`
	IDs     []string   `json:"-"`
}

// Prepare fills each row's provider and hosts, looking up MX records where
// needed. Call it before opening the transaction for Import, so DNS waits do
// not hold a transaction open. Rows that cannot be completed are returned as
// errors.
//
// In a test workspace every row becomes a simulated mailbox; elsewhere a
// simulated one is refused, so a live workspace never drops mail silently.
func (s *Service) Prepare(ctx context.Context, test bool, rows []Row) ([]Row, []RowError) {
	resolver := &cachedResolver{Resolver: s.Resolver, mx: map[string]mxResult{}}
	good := rows[:0:0]
	var bad []RowError
	for _, r := range rows {
		if test {
			Simulate(&r)
		} else if r.Provider == ProviderSimulated || r.SMTPHost == SimulatedHost || r.IMAPHost == SimulatedHost {
			bad = append(bad, RowError{Line: r.Line, Email: r.Email, Error: "simulated mailboxes are only for test workspaces"})
			continue
		}
		lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := Complete(lctx, resolver, &r)
		cancel()
		if err != nil {
			bad = append(bad, RowError{Line: r.Line, Email: r.Email, Error: err.Error()})
			continue
		}
		good = append(good, r)
	}
	return good, bad
}

// Import upserts prepared rows by email inside tx and queues a connection
// check for each mailbox and a DNS check for each domain.
func (s *Service) Import(ctx context.Context, tx pgx.Tx, workspaceID, source string, rows []Row) (ImportResult, error) {
	res := ImportResult{Errors: []RowError{}}
	test, err := IsTestWorkspace(ctx, tx, workspaceID)
	if err != nil {
		return res, err
	}
	if test {
		// Prepare did this already; a test workspace must never hold a
		// real mailbox, whatever the caller did.
		for i := range rows {
			Simulate(&rows[i])
		}
	}
	pools := map[string]string{}
	domains := map[string]bool{}
	servers := map[string]string{} // SMTP host to server id
	newServers := map[string]bool{}

	for i := range rows {
		r := &rows[i]
		created, id, err := s.upsert(ctx, tx, workspaceID, source, r, pools)
		if err != nil {
			return res, err
		}
		if created {
			res.Created++
		} else {
			res.Updated++
		}
		res.IDs = append(res.IDs, id)
		// Mailboxes outside Google and Microsoft are grouped by SMTP host,
		// so the IPs they share can be capped and watched together.
		var serverID *string
		if r.Provider == ProviderOther {
			host := strings.ToLower(r.SMTPHost)
			sid, ok := servers[host]
			if !ok {
				var added bool
				if sid, added, err = EnsureServer(ctx, tx, workspaceID, host); err != nil {
					return res, err
				}
				servers[host] = sid
				if added {
					newServers[sid] = true
				}
			}
			serverID = &sid
		}
		if _, err := tx.Exec(ctx, `UPDATE mailboxes SET server_id = $2 WHERE id = $1`, id, serverID); err != nil {
			return res, err
		}
		domains[strings.ToLower(r.Email[strings.LastIndexByte(r.Email, '@')+1:])] = true
		if err := s.QueueVerify(ctx, tx, workspaceID, id); err != nil {
			return res, err
		}
	}
	for d := range domains {
		if err := EnsureDomain(ctx, tx, workspaceID, d, "mailbox"); err != nil {
			return res, err
		}
		if err := s.QueueDomainCheck(ctx, tx, workspaceID, d); err != nil {
			return res, err
		}
	}
	for sid := range newServers {
		if err := s.QueueServerCheck(ctx, tx, workspaceID, sid); err != nil {
			return res, err
		}
	}
	return res, nil
}

// EnsureDomain adds a sending domain, not yet checked, unless the
// workspace already has it.
func EnsureDomain(ctx context.Context, q jobs.Querier, workspaceID, domain, source string) error {
	_, err := q.Exec(ctx, `
		INSERT INTO sending_domains (workspace_id, domain, spf, dkim, dmarc, checked_at, source)
		VALUES ($1, $2, 'pending', 'pending', 'pending', NULL, $3)
		ON CONFLICT (workspace_id, domain) DO NOTHING`, workspaceID, domain, source)
	return err
}

// recheckEvery is how often each domain's DNS and blocklists are checked
// again on their own, so a broken record or a listing shows up within a day.
const recheckEvery = 24 * time.Hour

// QueueDomainCheck schedules a DNS check for one domain.
func (s *Service) QueueDomainCheck(ctx context.Context, q jobs.Querier, workspaceID, domain string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobDomainCheck, WorkspaceID: &workspaceID, Payload: domainPayload{domain}, MaxAttempts: 3})
	return err
}

// QueueVerify schedules a connection check for one mailbox.
func (s *Service) QueueVerify(ctx context.Context, q jobs.Querier, workspaceID, id string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobVerify, WorkspaceID: &workspaceID, Payload: idPayload{id}, MaxAttempts: 1})
	return err
}

func (s *Service) upsert(ctx context.Context, tx pgx.Tx, ws, source string, r *Row, pools map[string]string) (created bool, id string, err error) {
	var poolID *string
	if r.Pool != "" {
		key := strings.ToLower(r.Pool)
		pid, ok := pools[key]
		if !ok {
			err := tx.QueryRow(ctx, `
				INSERT INTO mailbox_pools (workspace_id, name) VALUES ($1, $2)
				ON CONFLICT (workspace_id, lower(name)) DO UPDATE SET name = mailbox_pools.name
				RETURNING id`, ws, r.Pool).Scan(&pid)
			if err != nil {
				return false, "", err
			}
			pools[key] = pid
		}
		poolID = &pid
	}

	err = tx.QueryRow(ctx, `SELECT id FROM mailboxes WHERE workspace_id = $1 AND lower(email) = lower($2)`, ws, r.Email).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		created, id = true, newUUID()
	case err != nil:
		return false, "", err
	}
	creds, err := json.Marshal(stored{SMTPPassword: r.SMTPPassword, IMAPPassword: r.IMAPPassword, RefreshToken: r.RefreshToken})
	if err != nil {
		return false, "", err
	}
	sealed := s.Box.Seal(creds, []byte(id))

	if created {
		limit := r.DailyLimit
		if limit == 0 {
			limit = DefaultDailyLimit
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO mailboxes (id, workspace_id, pool_id, email, display_name, provider,
				smtp_host, smtp_port, smtp_username, imap_host, imap_port, imap_username,
				credentials, daily_limit, source, auth_method, oauth_connection_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
			id, ws, poolID, r.Email, r.DisplayName, r.Provider,
			r.SMTPHost, r.SMTPPort, r.SMTPUsername, r.IMAPHost, r.IMAPPort, r.IMAPUsername,
			sealed, limit, source, authMethod(r), r.ConnectionID)
	} else {
		// Re-importing a mailbox replaces its connection details and
		// re-verifies it. Pool, name and limit change only when given.
		_, err = tx.Exec(ctx, `
			UPDATE mailboxes SET
				provider = $3, smtp_host = $4, smtp_port = $5, smtp_username = $6,
				imap_host = $7, imap_port = $8, imap_username = $9, credentials = $10,
				pool_id = COALESCE($11, pool_id),
				display_name = CASE WHEN $12 = '' THEN display_name ELSE $12 END,
				daily_limit = CASE WHEN $13 = 0 THEN daily_limit ELSE $13 END,
				status = CASE WHEN status = 'paused' THEN status ELSE 'verifying' END,
				status_reason = NULL, smtp_ok = NULL, imap_ok = NULL, updated_at = now(),
				auth_method = $14, oauth_connection_id = $15
			WHERE id = $1 AND workspace_id = $2`,
			id, ws, r.Provider, r.SMTPHost, r.SMTPPort, r.SMTPUsername,
			r.IMAPHost, r.IMAPPort, r.IMAPUsername, sealed, poolID, r.DisplayName, r.DailyLimit,
			authMethod(r), r.ConnectionID)
	}
	if err != nil {
		return false, "", err
	}
	return created, id, nil
}

func authMethod(r *Row) string {
	if r.OAuth() {
		return "oauth"
	}
	return "password"
}

type idPayload struct {
	MailboxID string `json:"mailbox_id"`
}

type domainPayload struct {
	Domain string `json:"domain"`
}

// Account is a mailbox with its decrypted credentials, ready to connect.
type Account struct {
	ID          string
	WorkspaceID string
	Email       string
	DisplayName string
	Status      string
	// Purpose is outreach or support (mailboxes.purpose).
	Purpose  string
	BrandID  *string
	SMTP     Endpoint
	SMTPCred Credentials
	IMAP     Endpoint
	IMAPCred Credentials
}

// IsTestWorkspace reports whether a workspace is a test one, whose
// mailboxes are all simulated.
func IsTestWorkspace(ctx context.Context, q jobs.Querier, workspaceID string) (bool, error) {
	var test bool
	err := q.QueryRow(ctx, `SELECT test FROM workspaces WHERE id = $1`, workspaceID).Scan(&test)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return test, err
}

// ErrNotFound means no such mailbox in the workspace.
var ErrNotFound = errors.New("mailbox not found")

// ErrCredentials is stored credentials that can't be opened, for example
// sealed under another secret key. Only connecting the mailbox again
// fixes it.
var ErrCredentials = errors.New("the saved sign-in for this mailbox can't be read; connect it again")

// SignInLost reports whether err, from Load, means the mailbox's sign-in
// has stopped working for good: the credentials can't be opened, or the
// provider refused the OAuth refresh token (revoked, expired or consent
// removed). Trying again won't help until it is connected again. A
// network error or a provider outage is not lost.
func SignInLost(err error) bool {
	if errors.Is(err, ErrCredentials) {
		return true
	}
	var ce *ConnError
	if !errors.As(err, &ce) || ce.Stage != StageAuth {
		return false
	}
	var p interface{ Permanent() bool }
	return errors.As(err, &p) && p.Permanent()
}

// Load reads a mailbox and decrypts its credentials. For an OAuth mailbox
// it also gets an access token; failing that it returns a *ConnError at
// the auth stage, like a rejected password.
func (s *Service) Load(ctx context.Context, q jobs.Querier, workspaceID, id string) (*Account, error) {
	a := &Account{ID: id, WorkspaceID: workspaceID}
	var smtpHost, imapHost, provider, method string
	var smtpPort, imapPort int
	var sealed []byte
	var connID *string
	err := q.QueryRow(ctx, `
		SELECT email, display_name, status, purpose, brand_id::text, smtp_host, smtp_port, smtp_username,
		       imap_host, imap_port, imap_username, credentials, provider, auth_method, oauth_connection_id
		FROM mailboxes WHERE id = $1 AND workspace_id = $2`, id, workspaceID,
	).Scan(&a.Email, &a.DisplayName, &a.Status, &a.Purpose, &a.BrandID, &smtpHost, &smtpPort, &a.SMTPCred.Username,
		&imapHost, &imapPort, &a.IMAPCred.Username, &sealed, &provider, &method, &connID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	plain, err := s.Box.Open(sealed, []byte(id))
	if err != nil {
		return nil, fmt.Errorf("%w (decrypt mailbox %s: %v)", ErrCredentials, id, err)
	}
	var c stored
	if err := json.Unmarshal(plain, &c); err != nil {
		return nil, err
	}
	a.SMTP, a.IMAP = EndpointFor(smtpHost, smtpPort), EndpointFor(imapHost, imapPort)
	a.SMTPCred.Password, a.IMAPCred.Password = c.SMTPPassword, c.IMAPPassword
	if method != "oauth" {
		return a, nil
	}
	if s.Tokens == nil {
		return nil, &ConnError{"OAuth", StageAuth, errors.New("OAuth is not configured on this instance")}
	}
	tok, rotated, err := s.Tokens.Token(ctx, TokenRequest{
		MailboxID: id, WorkspaceID: workspaceID, Email: a.Email, Provider: provider,
		ConnectionID: connID, RefreshToken: c.RefreshToken,
	})
	if err != nil {
		var ce *ConnError
		if errors.As(err, &ce) {
			return nil, err
		}
		return nil, &ConnError{"OAuth", StageAuth, err}
	}
	if rotated != "" {
		c.RefreshToken = rotated
		b, _ := json.Marshal(c)
		if _, err := s.Pool.Exec(ctx, `UPDATE mailboxes SET credentials = $2 WHERE id = $1`, id, s.Box.Seal(b, []byte(id))); err != nil {
			return nil, err
		}
	}
	a.SMTPCred = Credentials{Username: a.Email, Token: tok}
	a.IMAPCred = Credentials{Username: a.Email, Token: tok}
	return a, nil
}

func (s *Service) verifyJob(ctx context.Context, job jobs.Job) error {
	var p idPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	acct, err := s.Load(ctx, s.Pool, *job.WorkspaceID, p.MailboxID)
	if errors.Is(err, ErrNotFound) {
		return nil // deleted since
	}
	var smtpErr, imapErr error
	var ce *ConnError
	switch {
	case errors.As(err, &ce):
		// No access token: both protocols fail the same way.
		smtpErr, imapErr = err, err
		var email string
		if err := s.Pool.QueryRow(ctx, `SELECT email FROM mailboxes WHERE id = $1`, p.MailboxID).Scan(&email); err != nil {
			return err
		}
		acct = &Account{ID: p.MailboxID, WorkspaceID: *job.WorkspaceID, Email: email}
	case err != nil:
		return err
	default:
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); smtpErr = s.Dialer.CheckSMTP(ctx, acct.SMTP, acct.SMTPCred) }()
		go func() { defer wg.Done(); imapErr = s.Dialer.CheckIMAP(ctx, acct.IMAP, acct.IMAPCred) }()
		wg.Wait()
	}

	status, reason, action := "active", "", "mailbox.verified"
	if err := errors.Join(smtpErr, imapErr); err != nil {
		status, reason, action = "error", err.Error(), "mailbox.verify_failed"
		if smtpErr == imapErr {
			reason = smtpErr.Error()
		}
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE mailboxes SET
				status = CASE WHEN status = 'paused' THEN status ELSE $3 END,
				status_reason = NULLIF($4, ''), smtp_ok = $5, imap_ok = $6,
				last_verified_at = now(), updated_at = now()
			WHERE id = $1 AND workspace_id = $2`,
			acct.ID, acct.WorkspaceID, status, reason, smtpErr == nil, imapErr == nil)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		data := map[string]any{"email": acct.Email, "smtp_ok": smtpErr == nil, "imap_ok": imapErr == nil}
		if reason != "" {
			data["reason"] = reason
		}
		return audit.Record(ctx, tx, audit.Event{
			WorkspaceID: acct.WorkspaceID, ActorType: "system", Action: action,
			TargetType: "mailbox", TargetID: acct.ID, Data: data,
		})
	})
}

func (s *Service) domainJob(ctx context.Context, job jobs.Job) error {
	var p domainPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || p.Domain == "" {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	test, err := IsTestWorkspace(ctx, s.Pool, *job.WorkspaceID)
	if err != nil {
		return err
	}
	var h DomainHealth
	if test {
		h = SimulatedDomain(p.Domain)
	} else {
		h = CheckDomain(ctx, s.Resolver, p.Domain)
	}
	lists, err := json.Marshal(h.Blocklists)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// A domain removed since the job was queued stays removed.
		tag, err := tx.Exec(ctx, `
			UPDATE sending_domains SET
				mx_provider = $3, mx_hosts = $4, spf = $5, spf_record = NULLIF($6, ''),
				dkim = $7, dkim_selector = NULLIF($8, ''), dmarc = $9, dmarc_record = NULLIF($10, ''),
				dmarc_policy = NULLIF($11, ''), blocklists = $12, checked_at = now()
			WHERE workspace_id = $1 AND domain = $2`,
			*job.WorkspaceID, h.Domain, h.MXProvider, h.MXHosts, h.SPF, h.SPFRecord, h.DKIM, h.DKIMSelector,
			h.DMARC, h.DMARCRecord, h.DMARCPolicy, lists)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		// One scheduled re-check per domain per day, however many checks
		// ran today.
		next := time.Now().Add(recheckEvery)
		_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{
			Kind: JobDomainCheck, WorkspaceID: job.WorkspaceID, Payload: p, MaxAttempts: 3, RunAt: next,
			IdempotencyKey: *job.WorkspaceID + ":" + h.Domain + ":" + next.UTC().Format("2006-01-02"),
		})
		return err
	})
}

// cachedResolver remembers MX answers for the length of one import, since
// thousands of mailboxes usually share a handful of domains.
type cachedResolver struct {
	Resolver
	mx map[string]mxResult
}

type mxResult struct {
	mx  []*net.MX
	err error
}

func (c *cachedResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	key := strings.ToLower(name)
	if r, ok := c.mx[key]; ok {
		return r.mx, r.err
	}
	mx, err := c.Resolver.LookupMX(ctx, name)
	c.mx[key] = mxResult{mx, err}
	return mx, err
}

// newUUID returns a random (version 4) UUID. The id is chosen before insert
// because it is bound into the credentials ciphertext.
func newUUID() string {
	var b [16]byte
	rand.Read(b[:]) //nolint:errcheck // never fails
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
