package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/models"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/llm"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Meaning-based search (migration 0201): records, replies, meetings and
// deals are turned into vectors with the workspace's own AI key, so search
// finds "too expensive for us" when someone asks for "unhappy with price".
//
// It needs pgvector in Postgres; without it the feature says so and stays
// off, and everything else works as before. It is on in every workspace
// with an AI connection that makes embeddings, unless an admin turns it
// off; what it spends is its own line in AI usage, with an explanation and
// a link to turn it off. A job keeps the vectors current: it embeds
// what is new or changed and deletes vectors of what is gone, so a deleted
// person's vector goes with them.

const (
	// taskSemanticSearch is the tool embeddings are booked under.
	taskSemanticSearch = "semantic_search"
	// jobSemanticEmbed embeds one workspace's new and changed items.
	jobSemanticEmbed = "search.embed"
	// semanticRunItems is the most items one job embeds; a job that
	// fills it queues the next straight away.
	semanticRunItems = 512
	// semanticCandidates is how many nearest vectors a search reads
	// before scopes and brands narrow them.
	semanticCandidates = 200
)

// semanticKinds are what meaning-based search covers, with the scope each
// needs. Only received messages are embedded: outreach is written from
// templates, and replies are where people say what they mean.
var semanticKinds = []struct{ kind, scope string }{
	{"record", auth.ScopeCustomersRead},
	{"message", auth.ScopeInboxRead},
	{"meeting", auth.ScopeOutcomesRead},
	{"deal", auth.ScopeOutcomesRead},
}

// semanticSources is, per kind, the text that is embedded and when it last
// changed, for workspace $1. A merged or archived record has none.
var semanticSources = map[string]string{
	"record": `SELECT r.id, r.updated_at AS changed_at,
			r.record_type || ': ' || coalesce(r.name, '') || coalesce(E'\n' || (
				SELECT string_agg(t.key || ': ' || (t.val #>> '{}'), E'\n' ORDER BY t.key)
				FROM jsonb_each(coalesce(r.attributes, '{}')) AS t(key, val) WHERE jsonb_typeof(t.val) = 'string'), '') AS body
		FROM records r WHERE r.workspace_id = $1 AND r.merged_into IS NULL AND r.archived_at IS NULL`,
	"message": `SELECT n.id, n.created_at AS changed_at, coalesce(n.subject, '') || E'\n' || left(coalesce(n.body, ''), 8000) AS body
		FROM inbound_messages n WHERE n.workspace_id = $1`,
	"meeting": `SELECT g.id, g.updated_at AS changed_at, coalesce(g.title, '') || E'\n' || coalesce(g.notes, '') AS body
		FROM meetings g WHERE g.workspace_id = $1`,
	"deal": `SELECT d.id, d.updated_at AS changed_at, coalesce(d.name, '') || E'\n' || coalesce(d.stage, '') AS body
		FROM deals d WHERE d.workspace_id = $1`,
}

// semanticState remembers whether the database has pgvector, so a request
// doesn't ask every time. A database without it is asked again every few
// minutes, so adding pgvector turns the feature on without a restart.
type semanticState struct {
	mu      sync.Mutex
	ready   bool
	checked time.Time
	// Whether pgvector can widen a filtered search (0.8 and later).
	iterative bool
	// Query embeddings, so asking the same thing again costs nothing.
	cache map[string]semanticCached
}

type semanticCached struct {
	vec []float32
	at  time.Time
}

// semanticReady reports whether meaning-based search can run here: the
// vectors' table exists, made now if pgvector has just been added.
func (s *Server) semanticReady(ctx context.Context) (bool, error) {
	st := &s.semantic
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.ready || (!st.checked.IsZero() && time.Since(st.checked) < 5*time.Minute) {
		return st.ready, nil
	}
	var ok bool
	if err := s.pool.QueryRow(ctx, `SELECT semantic_search_setup()`).Scan(&ok); err != nil {
		return false, err
	}
	st.ready, st.checked = ok, time.Now()
	metrics.SetSemanticAvailable(ok)
	if ok {
		var version string
		if err := s.pool.QueryRow(ctx, `SELECT extversion FROM pg_extension WHERE extname = 'vector'`).Scan(&version); err != nil {
			return false, err
		}
		st.iterative = versionAtLeast(version, 0, 8)
	}
	return ok, nil
}

func (s *Server) semanticIterative() bool {
	s.semantic.mu.Lock()
	defer s.semantic.mu.Unlock()
	return s.semantic.iterative
}

// versionAtLeast compares a version such as 0.8.1 with major.minor.
func versionAtLeast(v string, major, minor int) bool {
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return false
	}
	a, err1 := strconv.Atoi(parts[0])
	b, err2 := strconv.Atoi(parts[1])
	return err1 == nil && err2 == nil && (a > major || (a == major && b >= minor))
}

var errSemanticUnavailable = errAI{http.StatusServiceUnavailable, "semantic_search_unavailable",
	"Meaning-based search needs the pgvector extension in this server's Postgres database, and it isn't there. Self-hosting? See outflare.ai/docs for how to add it. Word search works without it."}

var errSemanticOff = errAI{http.StatusConflict, "semantic_search_off",
	"Meaning-based search was turned off in this workspace. An admin can turn it back on under Settings → Meaning-based search (PUT .../semantic-search)."}

// embedder is the workspace's connection that can make embeddings: the
// AI model chosen under Settings when it can, else the first that can.
// Claude has no embeddings, so a workspace on Claude alone has none.
func (s *Server) embedder(ctx context.Context, ws string) (aiChoice, llm.Config, error) {
	row, err := s.loadAISettings(ctx, s.pool, ws)
	if err != nil {
		return aiChoice{}, llm.Config{}, err
	}
	conns, err := s.aiConnections(ctx, ws)
	if err != nil {
		return aiChoice{}, llm.Config{}, err
	}
	conns = slices.DeleteFunc(conns, func(c connectors.Integration) bool {
		p, _ := llm.ProviderFor(c.Connector)
		return p.EmbedModel() == ""
	})
	if len(conns) == 0 {
		return aiChoice{}, llm.Config{}, errAI{http.StatusConflict, "connector_needed",
			"Meaning-based search needs an OpenAI, Gemini, Mistral or OpenRouter connection with your own API key (Claude doesn't offer embeddings). Add one under Connections (connector ids openai, gemini, mistral, openrouter)."}
	}
	conn := conns[0]
	if row.IntegrationID != nil {
		if i := slices.IndexFunc(conns, func(c connectors.Integration) bool { return c.ID == *row.IntegrationID }); i >= 0 {
			conn = conns[i]
		}
	}
	p, _ := llm.ProviderFor(conn.Connector)
	o, err := s.integrations.Open(ctx, s.pool, ws, conn.ID)
	if err != nil {
		return aiChoice{}, llm.Config{}, err
	}
	c := aiChoice{Provider: p, IntegrationID: conn.ID, Task: taskSemanticSearch, Price: p.EmbedPrice(), PriceSource: "list",
		MonthlyLimit: row.MonthlyLimit, TaskLimit: row.taskLimit(taskSemanticSearch),
		LimitWarnOnly: row.LimitWarnOnly, TaskWarnOnly: row.taskWarnOnly(taskSemanticSearch)}
	return c, models.Config(p, o.Client, o.Creds["api_key"], ""), nil
}

// embeddingConnectors are the AI connectors that make embeddings.
func embeddingConnectors() []string {
	var out []string
	for _, p := range llm.Providers {
		if p.EmbedModel() != "" {
			out = append(out, p.ID)
		}
	}
	return out
}

// embedModel names the model that made a vector, provider included:
// vectors from different models are never compared.
func embedModel(p llm.Provider) string { return p.ID + ":" + p.EmbedModel() }

// vectorLiteral is v as pgvector reads it.
func vectorLiteral(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 10)
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', 7, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// estimateCents is about what embedding texts of this many runes costs,
// four runes a token.
func estimateCents(p llm.Price, runes int) int64 {
	return int64(math.Ceil(float64(runes) / 4 * p.Input / 1e6))
}

// ---- Settings ----

func (s *Server) routesSemanticSearch(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/semantic-search", s.inWorkspace(auth.ScopeIntegrationsRead, s.getSemanticSearch))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/semantic-search", s.inWorkspace(auth.ScopeIntegrationsManage, s.putSemanticSearch))
}

func (s *Server) semanticEnabled(ctx context.Context, q rowQuerier, ws string) (bool, error) {
	var on bool
	err := q.QueryRow(ctx, `SELECT enabled FROM semantic_search_settings WHERE workspace_id = $1`, ws).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil // on unless turned off
	}
	return on, err
}

func (s *Server) semanticView(ctx context.Context, ws string) (map[string]any, error) {
	on, err := s.semanticEnabled(ctx, s.pool, ws)
	if err != nil {
		return nil, err
	}
	ready, err := s.semanticReady(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"enabled": on, "available": ready, "provider": nil, "model": nil,
		"embedded": 0, "waiting": 0, "problem": nil}
	if !ready {
		out["problem"] = aiProblemView{Code: errSemanticUnavailable.code, Message: errSemanticUnavailable.msg}
		return out, nil
	}
	c, _, err := s.embedder(ctx, ws)
	var e errAI
	switch {
	case errors.As(err, &e):
		out["problem"] = aiProblemView{Code: e.code, Message: e.msg}
		return out, nil
	case err != nil:
		return nil, err
	}
	model := embedModel(c.Provider)
	out["provider"], out["model"] = c.Provider.ID, c.Provider.EmbedModel()
	var embedded, waiting int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM search_embeddings WHERE workspace_id = $1 AND model = $2`, ws, model).Scan(&embedded); err != nil {
		return nil, err
	}
	for _, k := range semanticKinds {
		var n int64
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM (`+semanticSources[k.kind]+`) x
			LEFT JOIN search_embeddings e ON e.workspace_id = $1 AND e.kind = $2 AND e.item_id = x.id
			WHERE e.item_id IS NULL OR e.model <> $3 OR e.embedded_at < x.changed_at`, ws, k.kind, model).Scan(&n); err != nil {
			return nil, err
		}
		waiting += n
	}
	out["embedded"], out["waiting"] = embedded, waiting
	return out, nil
}

func (s *Server) getSemanticSearch(w http.ResponseWriter, r *http.Request) {
	v, err := s.semanticView(r.Context(), accessFrom(r.Context()).WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) putSemanticSearch(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "enabled is true or false")
		return
	}
	// Turning it on needs nothing else: it starts once pgvector and a
	// connection that makes embeddings are there, as GET says.
	ready, err := s.semanticReady(ctx)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO semantic_search_settings (workspace_id, enabled, updated_at, updated_by) VALUES ($1, $2, now(), $3)
			ON CONFLICT (workspace_id) DO UPDATE SET enabled = $2, updated_at = now(), updated_by = $3`,
			a.WorkspaceID, *req.Enabled, a.ActorID); err != nil {
			return err
		}
		deleted := int64(0)
		if *req.Enabled && ready {
			if err := enqueueSemanticEmbed(ctx, tx, a.WorkspaceID, "on:"+strconv.FormatInt(time.Now().UnixNano(), 36)); err != nil {
				return err
			}
		} else if ready {
			// Off means the vectors go too, so nothing is left of the
			// workspace's data in them.
			tag, err := tx.Exec(ctx, `DELETE FROM search_embeddings WHERE workspace_id = $1`, a.WorkspaceID)
			if err != nil {
				return err
			}
			deleted = tag.RowsAffected()
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "semantic_search.updated", "workspace", a.WorkspaceID,
			map[string]any{"enabled": *req.Enabled, "vectors_deleted": deleted})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.getSemanticSearch(w, r)
}

func enqueueSemanticEmbed(ctx context.Context, q jobs.Querier, ws, key string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: jobSemanticEmbed, WorkspaceID: &ws, MaxAttempts: 3,
		IdempotencyKey: "search.embed:" + ws + ":" + key})
	return err
}

// ---- Keeping the vectors current ----

func (s *Server) registerSemanticJobs(q *jobs.Queue) { q.Handle(jobSemanticEmbed, s.semanticEmbedJob) }

// RunSemanticSearch queues an embedding job for each workspace with
// meaning-based search on that has none waiting, every tick.
func (s *Server) RunSemanticSearch(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := s.QueueSemanticEmbeds(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "search.scheduler_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// QueueSemanticEmbeds runs one tick of RunSemanticSearch and reports how
// many jobs it queued.
func (s *Server) QueueSemanticEmbeds(ctx context.Context) (int, error) {
	ready, err := s.semanticReady(ctx)
	if err != nil {
		return 0, err
	}
	// Workspaces with it on (or never turned off) and a connection that
	// makes embeddings.
	// The key names the workspace's last embedding job, so servers ticking
	// together queue one job, not one each.
	rows, err := s.pool.Query(ctx, `SELECT w.id::text,
			NOT EXISTS (SELECT 1 FROM jobs j WHERE j.kind = $2 AND j.workspace_id = w.id AND j.status IN ('queued', 'running')),
			(SELECT coalesce(max(j.id), 0) FROM jobs j WHERE j.kind = $2 AND j.workspace_id = w.id)
		FROM workspaces w
		WHERE EXISTS (SELECT 1 FROM integrations i WHERE i.workspace_id = w.id AND i.connector = ANY ($1) AND i.status = 'connected')
			AND coalesce((SELECT st.enabled FROM semantic_search_settings st WHERE st.workspace_id = w.id), true)`,
		embeddingConnectors(), jobSemanticEmbed)
	if err != nil {
		return 0, err
	}
	type due struct{ ws, key string }
	var list []due
	enabled := 0
	var ws string
	var idle bool
	var last int64
	if _, err := pgx.ForEachRow(rows, []any{&ws, &idle, &last}, func() error {
		enabled++
		if idle {
			list = append(list, due{ws, "after:" + strconv.FormatInt(last, 10)})
		}
		return nil
	}); err != nil {
		return 0, err
	}
	metrics.SetSemanticWorkspaces(enabled)
	if !ready {
		return 0, nil
	}
	for _, d := range list {
		if err := enqueueSemanticEmbed(ctx, s.pool, d.ws, d.key); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

type semanticItem struct {
	kind, id, body, hash string
	same                 bool // the stored vector came from this text and model
}

func (s *Server) semanticEmbedJob(ctx context.Context, j jobs.Job) error {
	if j.WorkspaceID == nil {
		return fmt.Errorf("%w: no workspace", jobs.ErrPermanent)
	}
	ws := *j.WorkspaceID
	start := time.Now()
	ready, err := s.semanticReady(ctx)
	if err != nil || !ready {
		return err
	}
	on, err := s.semanticEnabled(ctx, s.pool, ws)
	if err != nil || !on {
		return err
	}
	ev := track.Event{Name: "search.embedded", ActorType: "system", Surface: "job", WorkspaceID: ws}
	fail := func(code string, err error) error {
		metrics.SemanticEmbedded("failed", 0, time.Since(start))
		ev.Name = "search.embed_failed"
		track.Fail(ctx, s.log, ev, code, err)
		return nil
	}
	// Vectors of what is gone (deleted, merged, archived) go first.
	tag, err := s.pool.Exec(ctx, `DELETE FROM search_embeddings e WHERE e.workspace_id = $1 AND CASE e.kind
		WHEN 'record' THEN NOT EXISTS (SELECT 1 FROM records r WHERE r.id = e.item_id AND r.workspace_id = $1 AND r.merged_into IS NULL AND r.archived_at IS NULL)
		WHEN 'message' THEN NOT EXISTS (SELECT 1 FROM inbound_messages n WHERE n.id = e.item_id AND n.workspace_id = $1)
		WHEN 'meeting' THEN NOT EXISTS (SELECT 1 FROM meetings g WHERE g.id = e.item_id AND g.workspace_id = $1)
		WHEN 'deal' THEN NOT EXISTS (SELECT 1 FROM deals d WHERE d.id = e.item_id AND d.workspace_id = $1)
		ELSE true END`, ws)
	if err != nil {
		return err
	}
	deleted := tag.RowsAffected()
	c, cfg, err := s.embedder(ctx, ws)
	var e errAI
	if errors.As(err, &e) {
		return fail(e.code, err)
	}
	if err != nil {
		return err
	}
	model := embedModel(c.Provider)
	ev.Props = map[string]any{"provider": c.Provider.ID}

	// What is new or changed, newest first.
	var items []semanticItem
	for _, k := range semanticKinds {
		if len(items) >= semanticRunItems {
			break
		}
		rows, err := s.pool.Query(ctx, `SELECT x.id::text, x.body, coalesce(e.content_hash, '') FROM (`+semanticSources[k.kind]+`) x
			LEFT JOIN search_embeddings e ON e.workspace_id = $1 AND e.kind = $2 AND e.item_id = x.id AND e.model = $3
			WHERE e.item_id IS NULL OR e.embedded_at < x.changed_at
			ORDER BY x.changed_at DESC LIMIT $4`, ws, k.kind, model, semanticRunItems-len(items))
		if err != nil {
			return err
		}
		got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (semanticItem, error) {
			it := semanticItem{kind: k.kind}
			var old string
			if err := row.Scan(&it.id, &it.body, &old); err != nil {
				return it, err
			}
			if utf8.RuneCountInString(it.body) > llm.EmbedMaxRunes {
				it.body = string([]rune(it.body)[:llm.EmbedMaxRunes])
			}
			sum := sha256.Sum256([]byte(model + "\x00" + it.body))
			it.hash = hex.EncodeToString(sum[:])
			it.same = old == it.hash
			return it, nil
		})
		if err != nil {
			return err
		}
		items = append(items, got...)
	}
	full := len(items) >= semanticRunItems

	// An item whose text didn't change keeps its vector.
	var todo []semanticItem
	unchanged := 0
	for _, it := range items {
		if it.same {
			if _, err := s.pool.Exec(ctx, `UPDATE search_embeddings SET embedded_at = now() WHERE workspace_id = $1 AND kind = $2 AND item_id = $3`,
				ws, it.kind, it.id); err != nil {
				return err
			}
			unchanged++
			continue
		}
		todo = append(todo, it)
	}

	var used llm.Usage
	embedded := 0
	var embedErr error
	code := ""
	for batch := range slices.Chunk(todo, c.Provider.EmbedBatch()) {
		runes := 0
		texts := make([]string, len(batch))
		for i, it := range batch {
			texts[i] = it.body
			runes += utf8.RuneCountInString(it.body)
		}
		if err := s.checkAIBudget(ctx, ws, c, estimateCents(c.Price, runes)); err != nil {
			embedErr, code = err, "ai_budget_reached"
			break
		}
		vecs, u, err := llm.Embed(ctx, cfg, texts)
		used.Add(u)
		if err != nil {
			embedErr, code = err, "connector_unavailable"
			if errors.Is(err, llm.ErrAuth) {
				code = "connector_auth"
			}
			break
		}
		for i, it := range batch {
			if _, err := s.pool.Exec(ctx, `INSERT INTO search_embeddings (workspace_id, kind, item_id, model, content_hash, embedding, embedded_at)
				VALUES ($1, $2, $3, $4, $5, $6::vector, now())
				ON CONFLICT (workspace_id, kind, item_id) DO UPDATE SET model = $4, content_hash = $5, embedding = $6::vector, embedded_at = now()`,
				ws, it.kind, it.id, model, it.hash, vectorLiteral(vecs[i])); err != nil {
				return err
			}
		}
		embedded += len(batch)
	}
	// What the run cost, booked once on the workspace's own provider bill.
	if err := recordAISpend(ctx, s.pool, c, ws, nil, nil, used, fmt.Sprintf("embed:%d", j.ID), "Meaning-based search"); err != nil {
		return err
	}
	ev.Props["items"], ev.Props["unchanged"], ev.Props["deleted"] = embedded, unchanged, deleted
	ev.Props["duration_ms"] = time.Since(start).Milliseconds()
	if embedErr != nil {
		return fail(code, embedErr)
	}
	metrics.SemanticEmbedded("success", embedded, time.Since(start))
	if embedded+unchanged > 0 || deleted > 0 {
		track.Emit(ctx, ev)
	}
	if full {
		return enqueueSemanticEmbed(ctx, s.pool, ws, "next:"+strconv.FormatInt(j.ID, 10))
	}
	return nil
}

// ---- Searching by meaning ----

// queryVector embeds what was asked, once per few minutes per model.
func (s *Server) queryVector(ctx context.Context, ws string, c aiChoice, cfg llm.Config, q string) ([]float32, error) {
	key := embedModel(c.Provider) + "\x00" + ws + "\x00" + q
	st := &s.semantic
	st.mu.Lock()
	if v, ok := st.cache[key]; ok && time.Since(v.at) < 10*time.Minute {
		st.mu.Unlock()
		return v.vec, nil
	}
	st.mu.Unlock()
	if err := s.checkAIBudget(ctx, ws, c, estimateCents(c.Price, len(q))); err != nil {
		return nil, err
	}
	vecs, u, err := llm.Embed(ctx, cfg, []string{q})
	if err := recordAISpend(ctx, s.pool, c, ws, nil, nil, u, "", "Meaning-based search"); err != nil {
		return nil, err
	}
	if err != nil {
		if errors.Is(err, llm.ErrAuth) {
			return nil, errAI{http.StatusUnprocessableEntity, "connector_auth", c.Provider.Name + " did not accept the workspace's API key. Check the connection under Connections."}
		}
		return nil, errAI{http.StatusBadGateway, "connector_unavailable", "Could not turn the search into a vector: " + llm.Scrub(err.Error())}
	}
	st.mu.Lock()
	if st.cache == nil || len(st.cache) > 1000 {
		st.cache = map[string]semanticCached{}
	}
	st.cache[key] = semanticCached{vec: vecs[0], at: time.Now()}
	st.mu.Unlock()
	return vecs[0], nil
}

// searchMeaning answers GET .../search?mode=meaning: the items nearest in
// meaning to q that the caller may see.
func (s *Server) searchMeaning(w http.ResponseWriter, r *http.Request, q string, want map[string]bool, limit int, picked *string, shared bool) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	if want["email"] {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kinds for meaning-based search are record, message, meeting and deal")
		return
	}
	may := map[string]bool{}
	var missing []string
	for _, k := range semanticKinds {
		if len(want) > 0 && !want[k.kind] {
			continue
		}
		if a.has(k.scope) {
			may[k.kind] = true
		} else {
			missing = append(missing, k.scope)
		}
	}
	if len(may) == 0 {
		writeError(w, r, http.StatusForbidden, "forbidden", s.missingScope(a, missing[0]))
		return
	}
	ready, err := s.semanticReady(ctx)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !ready {
		s.aiError(w, r, errSemanticUnavailable)
		return
	}
	on, err := s.semanticEnabled(ctx, s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !on {
		s.aiError(w, r, errSemanticOff)
		return
	}
	c, cfg, err := s.embedder(ctx, a.WorkspaceID)
	if s.aiError(w, r, err) {
		return
	}
	start := time.Now()
	vec, err := s.queryVector(ctx, a.WorkspaceID, c, cfg, q)
	if s.aiError(w, r, err) {
		return
	}
	var kinds []string
	for k := range may {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	var hits []searchHit
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		// A filtered search must still find enough: pgvector 0.8 widens
		// its scan until it has; older versions look further instead.
		if s.semanticIterative() {
			if _, err := tx.Exec(ctx, `SET LOCAL hnsw.iterative_scan = relaxed_order`); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `SET LOCAL hnsw.ef_search = 400`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			WITH near AS (
				SELECT e.kind, e.item_id, 1 - (e.embedding <=> $2::vector) AS score
				FROM search_embeddings e
				WHERE e.workspace_id = $1 AND e.model = $3 AND e.kind = ANY ($4)
				ORDER BY e.embedding <=> $2::vector LIMIT $7
			),
			hits AS (
				SELECT 'record' AS kind, r.id, r.id AS record_id, coalesce(r.name, '') AS title, r.record_type AS detail, r.updated_at AS at, n.score, '' AS snippet
				FROM near n JOIN records r ON n.kind = 'record' AND r.id = n.item_id
				WHERE r.workspace_id = $1 AND r.merged_into IS NULL AND r.archived_at IS NULL
					AND ($5::uuid[] IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY ($5)) AND ($9::uuid IS NULL OR r.brand_id = $9) AND (NOT $10 OR r.brand_id IS NULL)
				UNION ALL
				SELECT 'message', m.id, m.record_id, coalesce(m.subject, ''), 'received', m.received_at, n.score,
					left(regexp_replace(coalesce(m.body, ''), '\s+', ' ', 'g'), 240)
				FROM near n JOIN inbound_messages m ON n.kind = 'message' AND m.id = n.item_id
				LEFT JOIN mailboxes x ON x.id = m.mailbox_id
				WHERE m.workspace_id = $1 AND ($5::uuid[] IS NULL OR x.brand_id IS NULL OR x.brand_id = ANY ($5)) AND ($9::uuid IS NULL OR x.brand_id = $9) AND (NOT $10 OR x.brand_id IS NULL)
				UNION ALL
				SELECT 'meeting', g.id, g.record_id, coalesce(g.title, ''), coalesce(g.status, ''), coalesce(g.booked_at, g.created_at), n.score,
					left(regexp_replace(coalesce(g.notes, ''), '\s+', ' ', 'g'), 240)
				FROM near n JOIN meetings g ON n.kind = 'meeting' AND g.id = n.item_id
				LEFT JOIN sequences x ON x.id = g.sequence_id
				WHERE g.workspace_id = $1 AND ($5::uuid[] IS NULL OR x.brand_id IS NULL OR x.brand_id = ANY ($5)) AND ($9::uuid IS NULL OR x.brand_id = $9) AND (NOT $10 OR x.brand_id IS NULL)
				UNION ALL
				SELECT 'deal', d.id, d.record_id, coalesce(d.name, ''), coalesce(d.stage, ''), coalesce(d.opened_at, d.created_at), n.score, ''
				FROM near n JOIN deals d ON n.kind = 'deal' AND d.id = n.item_id
				LEFT JOIN sequences x ON x.id = d.sequence_id
				WHERE d.workspace_id = $1 AND ($5::uuid[] IS NULL OR x.brand_id IS NULL OR x.brand_id = ANY ($5)) AND ($9::uuid IS NULL OR x.brand_id = $9) AND (NOT $10 OR x.brand_id IS NULL)
			)
			SELECT h.kind, h.id::text, h.record_id::text, rr.name, h.title, h.detail, h.at, h.score::float8, h.snippet
			FROM hits h
			-- The record's name, for a caller who may read records and its brand.
			LEFT JOIN records rr ON rr.id = h.record_id AND rr.workspace_id = $1 AND $6
				AND ($5::uuid[] IS NULL OR rr.brand_id IS NULL OR rr.brand_id = ANY ($5))
			ORDER BY h.score DESC LIMIT $8`,
			a.WorkspaceID, vectorLiteral(vec), embedModel(c.Provider), kinds, brandList(a.Brands), a.has(auth.ScopeCustomersRead),
			semanticCandidates, limit, picked, shared)
		if err != nil {
			return err
		}
		hits, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (searchHit, error) {
			var h searchHit
			err := row.Scan(&h.Kind, &h.ID, &h.RecordID, &h.RecordName, &h.Title, &h.Detail, &h.At, &h.Rank, &h.Snippet)
			return h, err
		})
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if hits == nil {
		hits = []searchHit{}
	}
	found := map[string]int{}
	for _, h := range hits {
		found[h.Kind]++
	}
	ev := track.Event{Name: "search.ran", UserID: a.UserID, ActorID: a.ActorID, ActorType: a.ActorType, WorkspaceID: a.WorkspaceID, RequestID: RequestID(ctx),
		Props: map[string]any{"mode": "meaning", "results": len(hits), "kinds": found, "provider": c.Provider.ID, "duration_ms": time.Since(start).Milliseconds()}}
	if info := reqInfoFrom(ctx); info != nil {
		ev.Surface = surface(r, info)
	}
	track.Emit(ctx, ev)
	writeJSON(w, http.StatusOK, map[string]any{"data": hits})
}
