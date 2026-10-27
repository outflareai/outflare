package lists

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/leads"
	"github.com/outflareai/outflare/engine/internal/outcomes"
	"github.com/outflareai/outflare/engine/internal/usagelimits"
	"github.com/outflareai/outflare/engine/internal/verify"
)

// errStop ends a run early without failing it; the reason is on the run.
var errStop = errors.New("stopped")

type runner struct {
	s   *Service
	ws  string
	run Run

	searcher    c.PeopleSearcher
	search      *c.Opened
	waterfall   []*c.Opened
	stopReason  string
	pageCredits map[string]float64 // spent since the last commit, by integration
	page        int
}

// candidate is a person on their way into the list.
type candidate struct {
	person  c.Person
	score   *int
	reasons []string
	// Set when the person is already a lead (found earlier by id).
	leadID string
}

func (r *runner) skip(reason string, n int) {
	if n > 0 {
		r.run.Skipped[reason] += n
	}
}

func (r *runner) stop(format string, a ...any) error {
	r.stopReason = fmt.Sprintf(format, a...)
	return errStop
}

func (r *runner) execute(ctx context.Context) error {
	p := r.run.Params
	r.pageCredits = map[string]float64{}
	if r.run.Kind != KindEnrich {
		if p.SearchIntegrationID == "" {
			return invalid("choose a connected data provider to search")
		}
		o, err := r.open(ctx, p.SearchIntegrationID)
		if err != nil {
			return err
		}
		s, ok := o.Connector.(c.PeopleSearcher)
		if !ok {
			return invalid("%s cannot search for people; choose a provider that can", o.Connector.Definition().Name)
		}
		r.search, r.searcher = o, s
	}
	if r.run.Kind != KindPreview {
		ids := p.EnrichIntegrationIDs
		if len(ids) == 0 && r.search != nil {
			if _, ok := r.search.Connector.(c.PersonEnricher); ok {
				ids = []string{r.search.ID}
			}
		}
		for _, id := range ids {
			if r.search != nil && id == r.search.ID {
				r.waterfall = append(r.waterfall, r.search)
				continue
			}
			o, err := r.open(ctx, id)
			if err != nil {
				return err
			}
			if _, ok := o.Connector.(c.PersonEnricher); !ok {
				return invalid("%s cannot find emails or enrich people", o.Connector.Definition().Name)
			}
			r.waterfall = append(r.waterfall, o)
		}
	}
	switch r.run.Kind {
	case KindPreview:
		return r.preview(ctx)
	case KindBuild, KindRefresh:
		return r.build(ctx)
	case KindEnrich:
		return r.enrich(ctx)
	}
	return invalid("unknown run kind %q", r.run.Kind)
}

func (r *runner) open(ctx context.Context, id string) (*c.Opened, error) {
	if r.s.Store == nil {
		return nil, invalid("connectors are not set up on this server")
	}
	o, err := r.s.Store.Open(ctx, r.s.Pool, r.ws, id)
	if errors.Is(err, c.ErrNotFound) {
		return nil, invalid("a data provider this list uses was disconnected; choose another")
	}
	return o, err
}

// note records a provider call's outcome on the integration, so a revoked
// key shows on the Connectors page.
func (r *runner) note(ctx context.Context, o *c.Opened, err error) {
	if err != nil && c.KindOf(err) == c.ErrAuth {
		_ = r.s.Store.Checked(ctx, r.s.Pool, r.ws, o.ID, nil, err)
	}
}

func (r *runner) preview(ctx context.Context) error {
	if err := r.overLimit(ctx, r.search, 0); err != nil {
		return err
	}
	page, err := r.searcher.SearchPeople(ctx, r.search.Client, r.search.Creds, r.run.Params.Filters, "", previewSize)
	r.note(ctx, r.search, err)
	if err != nil {
		return err
	}
	r.charge(r.search, page.Credits)
	r.run.Found = len(page.People)
	r.run.TotalMatches = page.Total
	r.run.Sample = page.People
	if r.run.Sample == nil {
		r.run.Sample = []c.Person{}
	}
	return r.commit(ctx, nil)
}

func (r *runner) charge(o *c.Opened, credits float64) {
	if credits <= 0 {
		return
	}
	r.run.Credits += credits
	r.run.CreditsBy[o.ID] += credits
	r.pageCredits[o.ID] += credits
}

// overLimit stops the run when a call costing about next credits would
// take the connection past one of its usage limits (usagelimits), counting
// the credits this run spent and has not booked yet.
func (r *runner) overLimit(ctx context.Context, o *c.Opened, next float64) error {
	err := usagelimits.Check(ctx, r.s.Pool, r.ws, o.ID, "credit", r.pageCredits[o.ID]+next)
	var le *usagelimits.Error
	if errors.As(err, &le) {
		return r.stop("%s", le.Error())
	}
	return err
}

func (r *runner) overCap(next float64) bool {
	cap := r.run.Params.CreditCap
	return cap != nil && r.run.Credits+next > *cap+1e-9
}

func (r *runner) build(ctx context.Context) error {
	p := r.run.Params
	if p.Filters.Empty() {
		return invalid("add at least one search filter, such as titles or company domains")
	}
	if r.run.ListID == nil {
		return invalid("a build needs a list")
	}
	cursor := ""
	provider := r.search.Connector.Definition().ID
	for r.page = 1; r.page <= maxPagesPerRun; r.page++ {
		if cancelled, err := r.cancelled(ctx); err != nil || cancelled {
			return err
		}
		if r.overCap(0) {
			return r.stop("Reached the credit cap of %g.", *p.CreditCap)
		}
		if err := r.overLimit(ctx, r.search, 0); err != nil {
			return err
		}
		page, err := r.searcher.SearchPeople(ctx, r.search.Client, r.search.Creds, p.Filters, cursor, pageSize)
		r.note(ctx, r.search, err)
		if err != nil {
			return r.providerErr(ctx, err)
		}
		r.charge(r.search, page.Credits)
		if r.run.TotalMatches == nil {
			r.run.TotalMatches = page.Total
		}
		r.run.Found += len(page.People)

		cands, err := r.triage(ctx, provider, page.People)
		if err != nil {
			return err
		}
		stopErr := r.findEmails(ctx, cands)
		if stopErr != nil && !errors.Is(stopErr, errStop) {
			return r.providerErr(ctx, stopErr)
		}
		if err := r.add(ctx, provider, cands); err != nil {
			return err
		}
		if stopErr != nil {
			return stopErr
		}
		if r.run.Added >= p.MaxLeads {
			return r.stop("Added %d new leads, the most one run adds. Raise max_leads to add more.", p.MaxLeads)
		}
		if page.Next == "" {
			return nil
		}
		cursor = page.Next
	}
	return r.stop("Searched %d pages, the most one run reads. Refresh again to continue.", maxPagesPerRun)
}

// providerErr ends a run on a provider error, keeping what was added.
func (r *runner) providerErr(ctx context.Context, err error) error {
	switch c.KindOf(err) {
	case c.ErrQuota:
		_ = r.commit(ctx, nil)
		return r.stop("%s", err.Error())
	case c.ErrAuth:
		return err
	}
	return err
}

func (r *runner) cancelled(ctx context.Context) (bool, error) {
	var st string
	if err := r.s.Pool.QueryRow(ctx, `SELECT status FROM list_runs WHERE id = $1`, r.run.ID).Scan(&st); err != nil {
		return false, err
	}
	return st == StatusCancelled, nil
}

// triage drops people the list already has, recognises people found
// before by their provider id, and scores the rest, all before any credits
// are spent on them.
func (r *runner) triage(ctx context.Context, provider string, people []c.Person) ([]*candidate, error) {
	var ids []string
	for _, p := range people {
		if p.ProviderID != "" {
			ids = append(ids, p.ProviderID)
		}
	}
	type known struct {
		leadID string
		inList bool
	}
	seen := map[string]known{}
	if len(ids) > 0 {
		rows, err := r.s.Pool.Query(ctx, `
			SELECT s.external_id, s.lead_id,
			       EXISTS (SELECT 1 FROM lead_list_members m WHERE m.lead_id = s.lead_id AND m.list_id = $4)
			FROM lead_sources s WHERE s.workspace_id = $1 AND s.provider = $2 AND s.external_id = ANY($3)`,
			r.ws, provider, ids, *r.run.ListID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ext string
			var k known
			if err := rows.Scan(&ext, &k.leadID, &k.inList); err != nil {
				rows.Close()
				return nil, err
			}
			seen[ext] = k
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	var out []*candidate
	for _, p := range people {
		if k, ok := seen[p.ProviderID]; ok && p.ProviderID != "" {
			if k.inList {
				r.skip(SkipAlreadyInList, 1)
				continue
			}
			sc, why := Score(r.run.Params.ICP, p)
			out = append(out, &candidate{person: p, score: sc, reasons: why, leadID: k.leadID})
			continue
		}
		sc, why := Score(r.run.Params.ICP, p)
		if sc != nil && *sc < r.run.Params.MinScore {
			r.skip(SkipLowScore, 1)
			continue
		}
		out = append(out, &candidate{person: p, score: sc, reasons: why})
	}
	return out, nil
}

// findEmails runs the waterfall for candidates without an email. It
// returns errStop when the credit cap is reached.
func (r *runner) findEmails(ctx context.Context, cands []*candidate) error {
	for _, cd := range cands {
		if cd.leadID != "" || cd.person.Email != "" {
			continue
		}
		if r.run.Added+r.pending(cands) >= r.run.Params.MaxLeads {
			return nil
		}
		for _, o := range r.waterfall {
			e, err := r.lookup(ctx, o, cd.person)
			if errors.Is(err, errStop) {
				return err
			}
			if err != nil {
				if c.KindOf(err) == c.ErrInvalid {
					continue // this finder cannot use what we know; try the next
				}
				return err
			}
			if e.Found {
				cd.person = cd.person.Merge(e.Person)
				r.run.Enriched++
			}
			if cd.person.Email != "" && (!r.run.Params.VerifiedEmailsOnly || cd.person.EmailStatus == c.EmailVerified) {
				break
			}
		}
	}
	return nil
}

// pending counts candidates that already have what they need to be added.
func (r *runner) pending(cands []*candidate) int {
	n := 0
	for _, cd := range cands {
		if cd.leadID != "" || cd.person.Email != "" {
			n++
		}
	}
	return n
}

// lookup asks one provider about a person, through the cache.
func (r *runner) lookup(ctx context.Context, o *c.Opened, p c.Person) (c.Enriched, error) {
	enr := o.Connector.(c.PersonEnricher)
	q := c.QueryFor(p)
	provider := o.Connector.Definition().ID
	if r.search == nil || o.ID != r.search.ID {
		q.ProviderID = "" // ids belong to the provider that issued them
	}
	key := cacheKey(q)
	if key == "" {
		return c.Enriched{}, c.Errf(c.ErrInvalid, "nothing to look up")
	}
	var found bool
	var person []byte
	err := r.s.Pool.QueryRow(ctx, `SELECT found, person FROM enrichment_cache
		WHERE workspace_id = $1 AND provider = $2 AND lookup_key = $3 AND fetched_at > $4`,
		r.ws, provider, key, time.Now().Add(-cacheMaxAge)).Scan(&found, &person)
	if err == nil {
		var cached c.Person
		_ = json.Unmarshal(person, &cached)
		return c.Enriched{Person: cached, Found: found}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return c.Enriched{}, err
	}
	if r.overCap(1) {
		return c.Enriched{}, r.stop("Reached the credit cap of %g.", *r.run.Params.CreditCap)
	}
	if err := r.overLimit(ctx, o, 1); err != nil {
		return c.Enriched{}, err
	}
	e, err := enr.EnrichPerson(ctx, o.Client, o.Creds, q)
	r.note(ctx, o, err)
	if err != nil {
		return e, err
	}
	r.charge(o, e.Credits)
	b, _ := json.Marshal(e.Person)
	_, err = r.s.Pool.Exec(ctx, `
		INSERT INTO enrichment_cache (workspace_id, provider, lookup_key, found, person) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (workspace_id, provider, lookup_key) DO UPDATE SET found = EXCLUDED.found, person = EXCLUDED.person, fetched_at = now()`,
		r.ws, provider, key, e.Found, b)
	return e, err
}

func cacheKey(q c.PersonQuery) string {
	switch {
	case q.ProviderID != "":
		return "id:" + q.ProviderID
	case q.Email != "":
		return "email:" + strings.ToLower(q.Email)
	case q.LinkedInURL != "":
		return "linkedin:" + strings.ToLower(strings.TrimRight(q.LinkedInURL, "/"))
	case q.FirstName != "" && q.LastName != "" && (q.Domain != "" || q.Company != ""):
		return "name:" + strings.ToLower(q.FirstName+"|"+q.LastName+"|"+q.Domain+"|"+q.Company)
	}
	return ""
}

// add filters candidates by suppression, outreach history and
// verification, then adds them to the list in one transaction with the
// run's progress and the page's spend.
func (r *runner) add(ctx context.Context, provider string, cands []*candidate) error {
	p := r.run.Params
	var fresh []*candidate // new to the workspace, or new to this list
	byEmail := map[string]*candidate{}
	for _, cd := range cands {
		if cd.leadID != "" {
			fresh = append(fresh, cd)
			continue
		}
		e := strings.ToLower(strings.TrimSpace(cd.person.Email))
		switch {
		case e == "":
			r.skip(SkipNoEmail, 1)
			continue
		case p.VerifiedEmailsOnly && cd.person.EmailStatus != c.EmailVerified:
			r.skip(SkipUnverified, 1)
			continue
		}
		if _, dup := byEmail[e]; dup {
			continue
		}
		cd.person.Email = e
		byEmail[e] = cd
	}
	emails := make([]string, 0, len(byEmail))
	for e := range byEmail {
		emails = append(emails, e)
	}
	drop := func(set map[string]bool, reason string) {
		for e := range set {
			if _, ok := byEmail[e]; ok {
				delete(byEmail, e)
				r.skip(reason, 1)
			}
		}
	}
	if len(emails) > 0 {
		sup, err := r.matching(ctx, `SELECT lower(e) FROM unnest($2::text[]) e WHERE EXISTS (`+leads.SuppressedSQL("$1", "e")+`)`, emails)
		if err != nil {
			return err
		}
		drop(sup, SkipSuppressed)
		if !p.SkipInSequences && p.SkipContactedDays == 0 {
			goto verified
		}
		if p.SkipInSequences {
			in, err := r.matching(ctx, `SELECT lower(l.email) FROM enrollments en JOIN leads l ON l.id = en.lead_id
				WHERE en.workspace_id = $1 AND en.status = 'active' AND lower(l.email) = ANY($2)`, emails)
			if err != nil {
				return err
			}
			drop(in, SkipInSequence)
		}
		if p.SkipContactedDays > 0 {
			sent, err := r.matching(ctx, `SELECT DISTINCT lower(to_email) FROM messages
				WHERE workspace_id = $1 AND status = 'sent' AND lower(to_email) = ANY($2)
				AND sent_at > now() - make_interval(days => $3)`, emails, p.SkipContactedDays)
			if err != nil {
				return err
			}
			drop(sent, SkipContacted)
		}
	verified:
		if p.Verify && r.s.Verify != nil && len(byEmail) > 0 {
			blocked, err := r.verify(ctx, byEmail)
			if err != nil {
				return err
			}
			drop(blocked, SkipUndeliverable)
		}
	}
	for _, cd := range byEmail {
		fresh = append(fresh, cd)
	}
	// Respect max_leads within the page.
	if room := p.MaxLeads - r.run.Added; len(fresh) > room {
		fresh = fresh[:max(room, 0)]
	}
	return pgx.BeginFunc(ctx, r.s.Pool, func(tx pgx.Tx) error {
		added, err := r.save(ctx, tx, provider, fresh)
		if err != nil {
			return err
		}
		r.run.Added += added
		return r.commitTx(ctx, tx)
	})
}

func (r *runner) matching(ctx context.Context, sql string, args ...any) (map[string]bool, error) {
	rows, err := r.s.Pool.Query(ctx, sql, append([]any{r.ws}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out[e] = true
	}
	return out, rows.Err()
}

// verify checks addresses with the workspace's verifier and returns the
// ones its settings block.
func (r *runner) verify(ctx context.Context, byEmail map[string]*candidate) (map[string]bool, error) {
	emails := make([]string, 0, len(byEmail))
	for e := range byEmail {
		emails = append(emails, e)
	}
	if _, err := r.s.Verify.Verify(ctx, r.ws, emails, false); err != nil {
		return nil, err
	}
	set, err := verify.LoadSettings(ctx, r.s.Pool, r.ws)
	if err != nil {
		return nil, err
	}
	results, err := verify.List(ctx, r.s.Pool, r.ws, "", emails, len(emails), 0)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, res := range results {
		if b, _ := set.Blocked(res); b {
			out[res.Email] = true
		}
	}
	return out, nil
}

// leadFor turns a person into a lead row. Fields leads have no column for
// go into custom fields, usable in templates.
func leadFor(p c.Person) leads.Lead {
	custom := map[string]string{}
	put := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			custom[k] = v
		}
	}
	sen := p.Seniority
	if sen == "" {
		sen = InferSeniority(p.Title)
	}
	put("seniority", sen)
	put("location", p.Location())
	put("country", p.Country)
	put("industry", p.Company.Industry)
	if p.Company.Employees > 0 {
		put("employees", fmt.Sprint(p.Company.Employees))
	}
	put("company_domain", p.Company.Domain)
	put("company_linkedin_url", p.Company.LinkedInURL)
	website := p.Company.Website
	if website == "" && p.Company.Domain != "" {
		website = "https://" + p.Company.Domain
	}
	return leads.Lead{
		Email: strings.ToLower(p.Email), FirstName: p.FirstName, LastName: p.LastName, Company: p.Company.Name,
		Title: p.Title, Phone: p.Phone, LinkedInURL: p.LinkedInURL, Website: website, Custom: custom,
	}
}

// save adds candidates to the list, with their scores and sources.
func (r *runner) save(ctx context.Context, tx pgx.Tx, provider string, cands []*candidate) (int, error) {
	if len(cands) == 0 {
		return 0, nil
	}
	listID := *r.run.ListID
	var rows []leads.Lead
	for _, cd := range cands {
		if cd.leadID == "" {
			rows = append(rows, leadFor(cd.person))
		}
	}
	if len(rows) > 0 {
		unique, _ := leads.Dedupe(rows)
		if _, err := leads.Import(ctx, tx, r.ws, provider, nil, unique); err != nil {
			return 0, err
		}
	}
	added := 0
	for _, cd := range cands {
		reasons, _ := json.Marshal(cd.reasons)
		if cd.reasons == nil {
			reasons = []byte("[]")
		}
		var leadID string
		if cd.leadID != "" {
			leadID = cd.leadID
		} else if err := tx.QueryRow(ctx, `SELECT id FROM leads WHERE workspace_id = $1 AND lower(email) = $2`,
			r.ws, strings.ToLower(cd.person.Email)).Scan(&leadID); err != nil {
			return 0, err
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO lead_list_members (list_id, lead_id, score, score_reasons, run_id) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (list_id, lead_id) DO NOTHING`, listID, leadID, cd.score, reasons, r.run.ID)
		if err != nil {
			return 0, err
		}
		if tag.RowsAffected() == 0 {
			r.skip(SkipAlreadyInList, 1)
		} else {
			added++
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO lead_sources (workspace_id, lead_id, provider, external_id, integration_id, list_id, run_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (lead_id, provider) DO UPDATE SET last_seen_at = now(),
				external_id = CASE WHEN EXCLUDED.external_id = '' THEN lead_sources.external_id ELSE EXCLUDED.external_id END`,
			r.ws, leadID, provider, cd.person.ProviderID, r.search.ID, listID, r.run.ID); err != nil {
			return 0, err
		}
	}
	return added, nil
}

// enrich fills in missing fields on the list's leads.
func (r *runner) enrich(ctx context.Context) error {
	if r.run.ListID == nil {
		return invalid("enrichment needs a list")
	}
	if len(r.waterfall) == 0 {
		return invalid("choose at least one connected provider to enrich with")
	}
	type row struct {
		id string
		p  c.Person
	}
	rows, err := r.s.Pool.Query(ctx, `
		SELECT l.id, l.email, l.first_name, l.last_name, l.title, l.company, l.phone, l.linkedin_url, l.domain
		FROM leads l JOIN lead_list_members m ON m.lead_id = l.id
		WHERE m.list_id = $1 AND l.workspace_id = $2
		  AND (l.title = '' OR l.company = '' OR l.linkedin_url = '' OR l.first_name = '' OR l.last_name = '')
		ORDER BY m.added_at LIMIT $3`, *r.run.ListID, r.ws, r.run.Params.MaxLeads)
	if err != nil {
		return err
	}
	var todo []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.id, &x.p.Email, &x.p.FirstName, &x.p.LastName, &x.p.Title, &x.p.Company.Name, &x.p.Phone,
			&x.p.LinkedInURL, &x.p.Company.Domain); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, x)
	}
	rows.Close()
	r.run.Found = len(todo)
	for i, x := range todo {
		if i%25 == 0 {
			if cancelled, err := r.cancelled(ctx); err != nil || cancelled {
				return err
			}
		}
		merged := x.p
		hit := false
		for _, o := range r.waterfall {
			e, err := r.lookup(ctx, o, merged)
			if errors.Is(err, errStop) {
				_ = r.commit(ctx, nil)
				return err
			}
			if err != nil {
				if c.KindOf(err) == c.ErrInvalid {
					continue
				}
				_ = r.commit(ctx, nil)
				return r.providerErr(ctx, err)
			}
			if e.Found {
				e.Person.Email = "" // never replace the address the lead is known by
				merged = merged.Merge(e.Person)
				hit = true
			}
			if merged.Title != "" && merged.Company.Name != "" && merged.LinkedInURL != "" {
				break
			}
		}
		if !hit {
			continue
		}
		l := leadFor(merged)
		if _, err := r.s.Pool.Exec(ctx, `
			UPDATE leads SET
				first_name = CASE WHEN first_name = '' THEN $3 ELSE first_name END,
				last_name = CASE WHEN last_name = '' THEN $4 ELSE last_name END,
				title = CASE WHEN title = '' THEN $5 ELSE title END,
				company = CASE WHEN company = '' THEN $6 ELSE company END,
				phone = CASE WHEN phone = '' THEN $7 ELSE phone END,
				linkedin_url = CASE WHEN linkedin_url = '' THEN $8 ELSE linkedin_url END,
				website = CASE WHEN website = '' THEN $9 ELSE website END,
				custom = $10::jsonb || custom,
				updated_at = now()
			WHERE workspace_id = $1 AND id = $2`,
			r.ws, x.id, l.FirstName, l.LastName, l.Title, l.Company, l.Phone, l.LinkedInURL, l.Website, l.Custom); err != nil {
			return err
		}
		r.run.Enriched++
		if (i+1)%25 == 0 {
			if err := r.commit(ctx, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// commit saves the run's progress and the spend since the last commit.
func (r *runner) commit(ctx context.Context, _ any) error {
	return pgx.BeginFunc(ctx, r.s.Pool, func(tx pgx.Tx) error { return r.commitTx(ctx, tx) })
}

func (r *runner) commitTx(ctx context.Context, tx pgx.Tx) error {
	r.page++
	for id, credits := range r.pageCredits {
		if credits <= 0 {
			continue
		}
		provider := ""
		for _, o := range append([]*c.Opened{r.search}, r.waterfall...) {
			if o != nil && o.ID == id {
				provider = o.Connector.Definition().ID
			}
		}
		ref := fmt.Sprintf("list_run:%s:%s:%d", r.run.ID, id, r.page)
		units := credits
		integration := id
		note := "List " + r.run.Kind
		if _, _, err := outcomes.Record(ctx, tx, outcomes.Entry{
			WorkspaceID: r.ws, ProjectID: r.run.ProjectID, Category: outcomes.CategoryData, Provider: provider,
			IntegrationID: &integration, Units: &units, UnitName: "credit", OccurredAt: time.Now(),
			Source: outcomes.SourceAuto, Ref: &ref, Note: note, ActorID: r.run.RequestedBy,
		}); err != nil {
			return err
		}
	}
	r.pageCredits = map[string]float64{}
	skipped, _ := json.Marshal(r.run.Skipped)
	by, _ := json.Marshal(r.run.CreditsBy)
	sample, _ := json.Marshal(r.run.Sample)
	_, err := tx.Exec(ctx, `
		UPDATE list_runs SET found = $2, total_matches = $3, added = $4, enriched = $5, skipped = $6,
			credits = $7, credits_by = $8, sample = $9
		WHERE id = $1`, r.run.ID, r.run.Found, r.run.TotalMatches, r.run.Added, r.run.Enriched, skipped,
		r.run.Credits, by, sample)
	return err
}

// finish records the run's outcome and audits it.
func (r *runner) finish(ctx context.Context, runErr error) error {
	status, msg := StatusDone, ""
	switch {
	case runErr == nil, errors.Is(runErr, errStop):
	default:
		status, msg = StatusFailed, runErr.Error()
		var inv *ErrInvalid
		var ce *c.Error
		if !errors.As(runErr, &inv) && !errors.As(runErr, &ce) {
			r.s.Log.Error("list run failed", "run", r.run.ID, "err", runErr)
			msg = "Something went wrong on our side. Try again; if it keeps failing, check the server logs."
		}
	}
	return pgx.BeginFunc(ctx, r.s.Pool, func(tx pgx.Tx) error {
		if err := r.commitTx(ctx, tx); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE list_runs SET status = $2, error = NULLIF($3, ''), stop_reason = NULLIF($4, ''), finished_at = now()
			WHERE id = $1 AND status = 'running'`, r.run.ID, status, msg, r.stopReason)
		if err != nil || tag.RowsAffected() == 0 || r.run.Kind == KindPreview {
			return err
		}
		if r.run.Kind != KindEnrich {
			if _, err := tx.Exec(ctx, `UPDATE lead_lists SET updated_at = now() WHERE id = $1`, *r.run.ListID); err != nil {
				return err
			}
		}
		e := audit.Event{
			WorkspaceID: r.ws, ActorType: "system", Action: "lead_list." + r.run.Kind + "_" + status,
			TargetType: "lead_list", TargetID: *r.run.ListID,
			Data: map[string]any{"run_id": r.run.ID, "added": r.run.Added, "enriched": r.run.Enriched, "found": r.run.Found,
				"credits": r.run.Credits, "skipped": r.run.Skipped, "error": msg, "stop_reason": r.stopReason},
		}
		if r.run.RequestedBy != nil {
			e.ActorID = *r.run.RequestedBy
			_ = tx.QueryRow(ctx, `SELECT type FROM actors WHERE id = $1`, e.ActorID).Scan(&e.ActorType)
		}
		return audit.Record(ctx, tx, e)
	})
}
