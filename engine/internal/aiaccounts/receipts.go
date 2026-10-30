package aiaccounts

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/fxrates"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Receipts people forward (plans/ai-accounts, step 3): ReadReceipt reads
// what a receipt says, and ApplyReceipt matches it to an AI account and
// keeps that account's price, seats and renewal up to date. A receipt
// nobody can be sure of waits for a person (needs_review).

// Receipt is what a receipt says.
type Receipt struct {
	Vendor       string            `json:"vendor"`   // anthropic, openai, cursor, github
	Provider     string            `json:"provider"` // the AI tool, when it says
	ChargedOn    string            `json:"charged_on,omitempty"`
	AmountMinor  *int64            `json:"amount_minor,omitempty"`
	Currency     string            `json:"currency,omitempty"`
	Plan         string            `json:"plan,omitempty"`
	Seats        *int              `json:"seats,omitempty"`
	PeriodStart  string            `json:"period_start,omitempty"`
	PeriodEnd    string            `json:"period_end,omitempty"`
	AccountEmail string            `json:"account_email,omitempty"`
	Identifiers  map[string]string `json:"identifiers"`
}

// Message is the part of an email a receipt is read from.
type Message struct {
	From, To, Subject, Body string
	Date                    time.Time
	// DKIM lists the domains whose signature checked out.
	DKIM []string
}

func (m Message) signedBy(domain string) bool {
	for _, d := range m.DKIM {
		if d == domain || strings.HasSuffix(d, "."+domain) {
			return true
		}
	}
	return false
}

var (
	// Payment processors and app stores that send receipts for the vendors.
	processors  = []string{"stripe.com", "apple.com", "google.com"}
	vendorMarks = []struct {
		vendor, domain string
		names          *regexp.Regexp
	}{
		{"anthropic", "anthropic.com", regexp.MustCompile(`(?i)\b(anthropic|claude)\b`)},
		{"openai", "openai.com", regexp.MustCompile(`(?i)\b(openai|chatgpt)\b`)},
		{"cursor", "cursor.com", regexp.MustCompile(`(?i)\b(cursor|anysphere)\b`)},
		{"github", "github.com", regexp.MustCompile(`(?i)\bcopilot\b`)},
	}
	planPatterns = []struct {
		provider string
		re       *regexp.Regexp
	}{
		{"claude", regexp.MustCompile(`(?i)\bclaude\s+(pro|max(?:\s*\d+x)?|team(?:\s+(?:standard|premium))?|enterprise)\b`)},
		{"chatgpt", regexp.MustCompile(`(?i)\bchatgpt\s+(plus|pro|team|business|enterprise)\b`)},
		{"cursor", regexp.MustCompile(`(?i)\bcursor\s+(pro\+?|ultra|business|teams?)\b`)},
		{"copilot", regexp.MustCompile(`(?i)\bcopilot\s+(pro\+?|business|enterprise|individual)\b`)},
	}
	apiMarks  = regexp.MustCompile(`(?i)\b(api|credit grant|credits|usage)\b`)
	amountRe  = regexp.MustCompile(`(?im)^.{0,40}?\b(amount paid|total paid|amount charged|total due|total)\b[^\n\d]{0,30}?(?:([A-Z]{3})\s*)?(S\$|A\$|CA\$|NZ\$|HK\$|US\$|R\$|\$|€|£|¥|₹)?\s?([\d.,' ]*\d)\s*([A-Z]{3})?\s*$`)
	seatsRe   = regexp.MustCompile(`(?i)\b(?:qty|quantity|seats?)\s*[:x×]?\s*(\d{1,5})\b|\b(\d{1,5})\s+seats?\b`)
	periodRe  = regexp.MustCompile(`(?i)\b([A-Z][a-z]{2,8}\.? \d{1,2})(?:,? (\d{4}))?\s*(?:–|—|-|to)\s*([A-Z][a-z]{2,8}\.? \d{1,2}),? (\d{4})\b`)
	paidOnRe  = regexp.MustCompile(`(?i)\b(?:date paid|paid on|date of issue|invoice date|date)\s*:?\s*([A-Z][a-z]{2,8}\.? \d{1,2},? \d{4})\b`)
	billToRe  = regexp.MustCompile(`(?is)\b(?:bill(?:ed)? to|account|receipt for|sent to)\b.{0,120}?([a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,})`)
	invoiceRe = regexp.MustCompile(`(?i)\b(?:invoice|receipt)\s*(?:number|no\.?|#)\s*:?\s*#?([A-Z0-9]{4,12})-(\d{3,8})\b`)
	cardRe    = regexp.MustCompile(`(?i)\b(?:ending in|ends in|\*{2,4}|•{2,4}|x{4})\s*(\d{4})\b`)
	symbols   = map[string]string{"$": "USD", "US$": "USD", "€": "EUR", "£": "GBP", "S$": "SGD", "A$": "AUD", "CA$": "CAD", "NZ$": "NZD", "HK$": "HKD", "R$": "BRL", "¥": "JPY", "₹": "INR"}
)

// ReadReceipt reads a receipt from a message. ok is false when it isn't
// one from an AI company we know, signed by that company or by the
// payment processor or app store that sent it.
func ReadReceipt(m Message) (Receipt, bool) {
	r := Receipt{Identifiers: map[string]string{}}
	text := m.Subject + "\n" + m.Body
	for _, v := range vendorMarks {
		if m.signedBy(v.domain) {
			r.Vendor = v.vendor
			break
		}
	}
	if r.Vendor == "" {
		signedProcessor := false
		for _, p := range processors {
			signedProcessor = signedProcessor || m.signedBy(p)
		}
		if !signedProcessor {
			return r, false
		}
		for _, v := range vendorMarks {
			if v.names.MatchString(text) {
				r.Vendor = v.vendor
				break
			}
		}
		if r.Vendor == "" {
			return r, false
		}
	}

	for _, p := range planPatterns {
		if s := p.re.FindStringSubmatch(text); s != nil {
			r.Provider, r.Plan = p.provider, titleCase(s[1])
			break
		}
	}
	if r.Provider == "" {
		switch {
		case r.Vendor == "anthropic" && apiMarks.MatchString(text):
			r.Provider = "anthropic_api"
		case r.Vendor == "openai" && apiMarks.MatchString(text) && !strings.Contains(strings.ToLower(text), "chatgpt"):
			r.Provider = "openai_api"
		case r.Vendor == "cursor":
			r.Provider = "cursor"
		case r.Vendor == "github":
			r.Provider = "copilot"
		}
	}

	if a := amountRe.FindAllStringSubmatch(text, -1); len(a) > 0 {
		last := a[len(a)-1] // the final total, after any subtotals
		cur := last[2]
		if cur == "" {
			cur = last[5]
		}
		if cur == "" {
			cur = symbols[last[3]]
		}
		if cur == "" {
			cur = "USD"
		}
		if minor, ok := parseAmount(last[4], cur); ok {
			r.AmountMinor, r.Currency = &minor, cur
		}
	}
	if s := seatsRe.FindStringSubmatch(text); s != nil {
		n, _ := strconv.Atoi(s[1] + s[2])
		if n >= 1 && n <= 100000 {
			r.Seats = &n
		}
	}
	year := m.Date.Year()
	if year < 2000 {
		year = time.Now().Year()
	}
	if p := periodRe.FindStringSubmatch(text); p != nil {
		endYear, _ := strconv.Atoi(p[4])
		startYear := endYear
		if p[2] != "" {
			startYear, _ = strconv.Atoi(p[2])
		}
		start, ok1 := parseDay(p[1], startYear)
		end, ok2 := parseDay(p[3], endYear)
		if ok1 && ok2 && end.Before(start) && p[2] == "" {
			start = start.AddDate(-1, 0, 0)
		}
		if ok1 && ok2 && !end.Before(start) {
			r.PeriodStart, r.PeriodEnd = start.Format(time.DateOnly), end.Format(time.DateOnly)
		}
	}
	if d := paidOnRe.FindStringSubmatch(text); d != nil {
		if t, err := time.Parse("Jan 2 2006", normalizeDay(d[1])); err == nil {
			r.ChargedOn = t.Format(time.DateOnly)
		} else if t, err := time.Parse("January 2 2006", normalizeDay(d[1])); err == nil {
			r.ChargedOn = t.Format(time.DateOnly)
		}
	}
	if r.ChargedOn == "" && !m.Date.IsZero() {
		r.ChargedOn = m.Date.UTC().Format(time.DateOnly)
	}
	if e := billToRe.FindStringSubmatch(m.Body); e != nil {
		r.AccountEmail = strings.ToLower(strings.TrimRight(e[1], "."))
	}
	if v := invoiceRe.FindStringSubmatch(text); v != nil {
		r.Identifiers["invoice_prefix"] = r.Vendor + ":" + strings.ToUpper(v[1])
	}
	if c := cardRe.FindStringSubmatch(text); c != nil {
		r.Identifiers["card_last4"] = r.Vendor + ":" + c[1]
	}
	return r, true
}

func titleCase(s string) string {
	f := strings.Fields(strings.ToLower(s))
	for i, w := range f {
		if len(w) > 0 && !(strings.HasSuffix(w, "x") && w[0] >= '0' && w[0] <= '9') {
			f[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(f, " ")
}

func normalizeDay(s string) string {
	return strings.Join(strings.Fields(strings.NewReplacer(",", " ", ".", "").Replace(s)), " ")
}

func parseDay(s string, year int) (time.Time, bool) {
	s = normalizeDay(s) + " " + strconv.Itoa(year)
	for _, layout := range []string{"Jan 2 2006", "January 2 2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// parseAmount reads 1,234.56 or 1.234,56 as minor units of cur.
func parseAmount(s, cur string) (int64, bool) {
	s = strings.NewReplacer(" ", "", "'", "").Replace(s)
	if i, j := strings.LastIndex(s, ","), strings.LastIndex(s, "."); i > j && len(s)-i == 3 {
		s = strings.ReplaceAll(strings.ReplaceAll(s, ".", ""), ",", ".")
	} else {
		s = strings.ReplaceAll(s, ",", "")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || f > 1e9 {
		return 0, false
	}
	return int64(math.Round(f * math.Pow10(fxrates.MinorDigits(cur)))), true
}

// vendorProviders are the AI tools a vendor's receipts can be for.
var vendorProviders = map[string][]string{
	"anthropic": {"claude", "anthropic_api"},
	"openai":    {"chatgpt", "openai_api"},
	"cursor":    {"cursor"},
	"github":    {"copilot"},
}

// Applied is what ApplyReceipt did.
type Applied struct {
	ReceiptID string
	Status    string // matched or needs_review
	AccountID string
	MatchedBy string
}

// ApplyReceipt stores a receipt forwarded by forwarderID (a member, or
// empty), matches it to an account and updates that account, all in tx:
// first by the account email it names, then by an identifier a matched
// receipt had before, then by the forwarder's only account for that AI
// tool, then by the only account due to renew around the charge. Anything
// else waits for a person.
func ApplyReceipt(ctx context.Context, tx pgx.Tx, workspaceID, inboundEmailID, forwarderID string, r Receipt) (Applied, error) {
	providers := vendorProviders[r.Vendor]
	if r.Provider != "" {
		providers = []string{r.Provider}
	}
	// In US cents: as charged, or converted with that day's rate. Without
	// a rate yet it stays empty until ConvertPending fills it in.
	var usd *int64
	if r.AmountMinor != nil && r.Currency != "" {
		var err error
		if usd, err = fxrates.ToUSD(ctx, tx, r.Currency, *r.AmountMinor, r.ChargedOn); err != nil {
			return Applied{}, err
		}
	}
	match, by := "", ""
	one := func(sql string, args ...any) (string, error) {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return "", err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil || len(ids) != 1 {
			return "", err
		}
		return ids[0], nil
	}
	live := `a.workspace_id = $1 AND a.cancelled_at IS NULL AND a.provider = ANY($2)`
	var err error
	if r.AccountEmail != "" {
		if match, err = one(`SELECT a.id::text FROM ai_accounts a WHERE `+live+` AND lower(a.account_email) = $3`, workspaceID, providers, r.AccountEmail); err != nil {
			return Applied{}, err
		}
		by = "account_email"
	}
	if match == "" {
		for _, kind := range []string{"invoice_prefix", "card_last4"} {
			v := r.Identifiers[kind]
			if v == "" {
				continue
			}
			if match, err = one(`SELECT a.id::text FROM ai_receipt_ids i JOIN ai_accounts a ON a.id = i.account_id
				WHERE i.workspace_id = $1 AND i.kind = $3 AND i.value = $4 AND a.cancelled_at IS NULL AND a.provider = ANY($2)`,
				workspaceID, providers, kind, v); err != nil {
				return Applied{}, err
			}
			if match != "" {
				by = "known_id"
				break
			}
		}
	}
	if match == "" && forwarderID != "" {
		if match, err = one(`SELECT a.id::text FROM ai_accounts a JOIN ai_account_members m ON m.account_id = a.id
			WHERE `+live+` AND m.user_id::text = $3`, workspaceID, providers, forwarderID); err != nil {
			return Applied{}, err
		}
		by = "forwarder"
	}
	if match == "" && r.ChargedOn != "" {
		if match, err = one(`SELECT a.id::text FROM ai_accounts a WHERE `+live+`
			AND a.renews_on BETWEEN $3::date - 5 AND $3::date + 5
			AND ($4::bigint IS NULL OR a.price_cents IS NULL OR abs(a.price_cents - $4) <= greatest(100, a.price_cents / 10))`,
			workspaceID, providers, r.ChargedOn, usd); err != nil {
			return Applied{}, err
		}
		by = "expected_renewal"
	}
	if match == "" {
		by = ""
	}
	out := Applied{Status: "needs_review"}
	if match != "" {
		out = Applied{Status: "matched", AccountID: match, MatchedBy: by}
	}
	id, err := insertReceipt(ctx, tx, workspaceID, inboundEmailID, forwarderID, r, usd, out)
	if err != nil {
		return out, err
	}
	out.ReceiptID = id
	props := map[string]any{"vendor": r.Vendor, "provider": r.Provider, "status": out.Status}
	if out.Status == "matched" {
		props["matched_by"] = out.MatchedBy
		if err := applyToAccount(ctx, tx, workspaceID, match, r, usd); err != nil {
			return out, err
		}
		if err := learnIDs(ctx, tx, workspaceID, match, r); err != nil {
			return out, err
		}
	}
	action := "ai_receipt.matched"
	if out.Status == "needs_review" {
		action = "ai_receipt.needs_review"
	}
	return out, audit.Record(ctx, tx, audit.Event{WorkspaceID: workspaceID, ActorType: "system", Action: action,
		TargetType: "ai_receipt", TargetID: id, Data: props, Props: props})
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func insertReceipt(ctx context.Context, tx pgx.Tx, workspaceID, inboundEmailID, forwarderID string, r Receipt, usd *int64, a Applied) (string, error) {
	ids, _ := json.Marshal(r.Identifiers)
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO ai_receipts (workspace_id, inbound_email_id, vendor, provider, forwarded_by, charged_on, amount_minor, currency, usd_cents,
			plan, seats, period_start, period_end, account_email, identifiers, account_id, matched_by, status)
		VALUES ($1, $2::uuid, $3, $4, $5::uuid, $6::date, $7, $8, $9, $10, $11, $12::date, $13::date, $14, $15, $16::uuid, $17, $18)
		RETURNING id::text`,
		workspaceID, nilIfEmpty(inboundEmailID), r.Vendor, nilIfEmpty(r.Provider), nilIfEmpty(forwarderID), nilIfEmpty(r.ChargedOn),
		r.AmountMinor, nilIfEmpty(r.Currency), usd, nilIfEmpty(r.Plan), r.Seats, nilIfEmpty(r.PeriodStart), nilIfEmpty(r.PeriodEnd),
		nilIfEmpty(r.AccountEmail), ids, nilIfEmpty(a.AccountID), nilIfEmpty(a.MatchedBy), a.Status).Scan(&id)
	return id, err
}

// applyToAccount keeps the account's details up to date from a receipt:
// its next renewal (the end of the period it paid for, or a cycle after the
// charge), plus the plan, seats, price in US dollars and account email the
// receipt names. A renewal earlier than the one it has is left alone.
func applyToAccount(ctx context.Context, tx pgx.Tx, workspaceID, accountID string, r Receipt, usd *int64) error {
	next := r.PeriodEnd
	if next == "" && r.ChargedOn != "" {
		var cycle *string
		if err := tx.QueryRow(ctx, `SELECT billing_cycle FROM ai_accounts WHERE id::text = $1`, accountID).Scan(&cycle); err != nil {
			return err
		}
		if t, err := time.Parse(time.DateOnly, r.ChargedOn); err == nil && cycle != nil {
			switch *cycle {
			case "monthly":
				next = t.AddDate(0, 1, 0).Format(time.DateOnly)
			case "yearly":
				next = t.AddDate(1, 0, 0).Format(time.DateOnly)
			}
		}
	}
	// A price only from a subscription's own charge, not usage or credits.
	price := usd
	if strings.HasSuffix(r.Provider, "_api") {
		price = nil
	}
	_, err := tx.Exec(ctx, `
		UPDATE ai_accounts SET
			renews_on = CASE WHEN $3::date IS NOT NULL AND (renews_on IS NULL OR $3::date > renews_on) THEN $3::date ELSE renews_on END,
			plan = coalesce($4, plan),
			seats = coalesce($5, seats),
			price_cents = coalesce($6, price_cents),
			account_email = coalesce(account_email, $7),
			updated_at = now()
		WHERE workspace_id = $1 AND id::text = $2`,
		workspaceID, accountID, nilIfEmpty(next), nilIfEmpty(r.Plan), r.Seats, price, nilIfEmpty(r.AccountEmail))
	return err
}

// learnIDs remembers a matched receipt's identifiers for its account. One
// that turns out to belong to two accounts (a card that pays for both) is
// forgotten, so it never matches the wrong one.
func learnIDs(ctx context.Context, tx pgx.Tx, workspaceID, accountID string, r Receipt) error {
	for kind, v := range r.Identifiers {
		var owner string
		err := tx.QueryRow(ctx, `INSERT INTO ai_receipt_ids (workspace_id, kind, value, account_id) VALUES ($1, $2, $3, $4::uuid)
			ON CONFLICT (workspace_id, kind, value) DO UPDATE SET kind = EXCLUDED.kind RETURNING account_id::text`,
			workspaceID, kind, v, accountID).Scan(&owner)
		if err != nil {
			return err
		}
		if owner != accountID {
			if _, err := tx.Exec(ctx, `DELETE FROM ai_receipt_ids WHERE workspace_id = $1 AND kind = $2 AND value = $3`, workspaceID, kind, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// ErrReceiptNotFound is returned for a receipt that isn't waiting for a
// person.
var ErrReceiptNotFound = errors.New("no receipt waiting for review with that id")

// ResolveReceipt settles a receipt that needs review: assigns it to an
// account (updating the account and learning its identifiers), or ignores
// it when accountID is empty. byUserID is who did it.
func ResolveReceipt(ctx context.Context, tx pgx.Tx, workspaceID, receiptID, accountID, byUserID string) (Receipt, error) {
	var r Receipt
	var ids []byte
	var usd *int64
	err := tx.QueryRow(ctx, `SELECT vendor, coalesce(provider, ''), coalesce(charged_on::text, ''), amount_minor, coalesce(currency, ''), usd_cents,
		coalesce(plan, ''), seats, coalesce(period_start::text, ''), coalesce(period_end::text, ''), coalesce(account_email, ''), identifiers
		FROM ai_receipts WHERE workspace_id = $1 AND id::text = $2 AND status = 'needs_review' FOR UPDATE`, workspaceID, receiptID).
		Scan(&r.Vendor, &r.Provider, &r.ChargedOn, &r.AmountMinor, &r.Currency, &usd, &r.Plan, &r.Seats, &r.PeriodStart, &r.PeriodEnd, &r.AccountEmail, &ids)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrReceiptNotFound
	}
	if err != nil {
		return r, err
	}
	_ = json.Unmarshal(ids, &r.Identifiers)
	status, by := "ignored", (*string)(nil)
	if accountID != "" {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ai_accounts WHERE workspace_id = $1 AND id::text = $2)`, workspaceID, accountID).Scan(&ok); err != nil {
			return r, err
		}
		if !ok {
			return r, ErrNotFound
		}
		status, by = "matched", nilIfEmpty("person")
		if err := applyToAccount(ctx, tx, workspaceID, accountID, r, usd); err != nil {
			return r, err
		}
		if err := learnIDs(ctx, tx, workspaceID, accountID, r); err != nil {
			return r, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE ai_receipts SET status = $3, account_id = $4::uuid, matched_by = $5, resolved_by = $6::uuid, resolved_at = now()
		WHERE workspace_id = $1 AND id::text = $2`, workspaceID, receiptID, status, nilIfEmpty(accountID), by, nilIfEmpty(byUserID))
	return r, err
}

// ConvertPending gives receipts charged in another currency their amount
// in US cents once a rate for their day exists, and the price of the
// accounts they are the latest matched receipt for (subscriptions only, as
// in applyToAccount). It runs after each fetch of exchange rates, across
// workspaces, and returns how many receipts it converted.
func ConvertPending(ctx context.Context, q fxrates.Querier) (int64, error) {
	tag, err := q.Exec(ctx, `
		WITH conv AS (
			SELECT r.id, round(r.amount_minor::numeric / (CASE WHEN r.currency IN ('JPY', 'KRW', 'ISK', 'HUF') THEN 1 ELSE 100 END) * x.usd_per_unit * 100)::bigint AS usd
			FROM ai_receipts r
			CROSS JOIN LATERAL (
				SELECT e.usd_per_unit FROM exchange_rates e
				WHERE e.currency = r.currency AND e.day <= coalesce(r.charged_on, r.created_at::date) AND e.day > coalesce(r.charged_on, r.created_at::date) - $1::int
				ORDER BY e.day DESC LIMIT 1) x
			WHERE r.usd_cents IS NULL AND r.amount_minor IS NOT NULL AND r.currency IS NOT NULL AND r.currency <> 'USD'
		), done AS (
			UPDATE ai_receipts r SET usd_cents = conv.usd FROM conv WHERE r.id = conv.id
			RETURNING r.id, r.workspace_id, r.account_id, r.provider, r.charged_on, r.usd_cents, r.status
		), priced AS (
			UPDATE ai_accounts a SET price_cents = d.usd_cents, updated_at = now()
			FROM done d
			WHERE d.status = 'matched' AND a.id = d.account_id AND a.workspace_id = d.workspace_id AND coalesce(d.provider, '') NOT LIKE '%\_api'
				AND NOT EXISTS (SELECT 1 FROM ai_receipts o WHERE o.workspace_id = d.workspace_id AND o.account_id = d.account_id
					AND o.status = 'matched' AND o.id <> d.id AND o.charged_on > d.charged_on)
			RETURNING a.id
		)
		SELECT 1 FROM done`, int(fxrates.MaxAge.Hours()/24))
	if err != nil {
		return 0, err
	}
	metrics.ReceiptsConverted(tag.RowsAffected())
	return tag.RowsAffected(), nil
}
