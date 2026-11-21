// Package intercomtest is a fake Intercom API for tests: one workspace with
// teammates, a team, contacts and conversations that a test can change.
package intercomtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Token is the access token the fake accepts.
const Token = "intercom-test-not-a-real-token"

// App is the fake workspace's app id.
const App = "northpeak01"

// Server is a fake Intercom.
type Server struct {
	*httptest.Server
	mu            sync.Mutex
	conversations map[string]map[string]any
	contacts      map[string]map[string]any
	// Gets counts conversation reads by id.
	Gets map[string]int
	// Searches counts search calls.
	Searches int
}

// New starts a fake with two teammates, a team, two contacts and no
// conversations.
func New() *Server {
	s := &Server{conversations: map[string]map[string]any{}, contacts: map[string]map[string]any{}, Gets: map[string]int{}}
	s.contacts["c1"] = map[string]any{"type": "contact", "id": "c1", "role": "user", "email": "ana@acme.example", "name": "Ana Silva"}
	s.contacts["c2"] = map[string]any{"type": "contact", "id": "c2", "role": "lead", "email": nil, "name": nil}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Put adds or replaces a conversation (as Intercom returns it from GET).
func (s *Server) Put(conv map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conversations[conv["id"].(string)] = conv
}

// Conversation is a ready-made conversation: a customer (c1) asks by chat,
// Fin answers from an article, then a teammate (a1) replies and closes it,
// with a note in between.
func Conversation(id string, updated int64) map[string]any {
	return map[string]any{
		"type": "conversation", "id": id, "title": nil, "created_at": updated - 600, "updated_at": updated,
		"state": "closed", "open": false, "priority": "high", "admin_assignee_id": 1001, "team_assignee_id": "7",
		"source": map[string]any{"type": "conversation", "id": "s-" + id, "delivered_as": "customer_initiated", "subject": "",
			"body": "Does Northpeak export reports to CSV?", "author": map[string]any{"type": "user", "id": "c1", "name": "Ana Silva", "email": "ana@acme.example"},
			"attachments": []any{}},
		"contacts":              map[string]any{"type": "contact.list", "contacts": []any{map[string]any{"type": "contact", "id": "c1"}}},
		"tags":                  map[string]any{"type": "tag.list", "tags": []any{map[string]any{"type": "tag", "id": "9", "name": "exports"}}},
		"statistics":            map[string]any{"first_admin_reply_at": updated - 300, "last_close_at": updated - 60, "count_reopens": 1},
		"conversation_rating":   map[string]any{"rating": 5, "remark": "Quick and clear", "created_at": updated - 30},
		"ai_agent_participated": true,
		"ai_agent": map[string]any{"source_type": "workflow", "source_title": "Support bot", "last_answer_type": "ai_answer",
			"resolution_state": "escalated", "rating": 3, "rating_remark": "Partly helpful",
			"content_sources": map[string]any{"type": "content_source.list", "total_count": 1, "content_sources": []any{
				map[string]any{"content_type": "article", "title": "Exporting reports", "url": "https://help.example.com/exports", "locale": "en"}}}},
		"conversation_parts": map[string]any{"type": "conversation_part.list", "total_count": 5, "conversation_parts": []any{
			map[string]any{"type": "conversation_part", "id": id + "-p1", "part_type": "comment", "body": "Yes: Reports, Export, CSV.", "created_at": updated - 500,
				"author": map[string]any{"type": "bot", "id": "f1", "name": "Fin", "from_ai_agent": true, "is_ai_answer": true}},
			map[string]any{"type": "conversation_part", "id": id + "-p2", "part_type": "assignment", "body": nil, "created_at": updated - 450,
				"author": map[string]any{"type": "bot", "id": "f1", "name": "Fin"}},
			map[string]any{"type": "conversation_part", "id": id + "-p3", "part_type": "note", "body": "Customer is on the Pro plan.", "created_at": updated - 400,
				"author": map[string]any{"type": "admin", "id": "1001", "name": "Sam Lee"}},
			map[string]any{"type": "conversation_part", "id": id + "-p4", "part_type": "comment", "body": "And scheduled exports too?", "created_at": updated - 350,
				"author": map[string]any{"type": "user", "id": "c1", "name": "Ana Silva"}},
			map[string]any{"type": "conversation_part", "id": id + "-p5", "part_type": "close", "body": "Yes, under Reports, Schedule.", "created_at": updated - 300,
				"author": map[string]any{"type": "admin", "id": "1001", "name": "Sam Lee"}},
		}},
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+Token {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"type":"error.list","errors":[{"code":"unauthorized","message":"Access Token Invalid"}]}`))
		return
	}
	if r.Header.Get("Intercom-Version") == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/me":
		out(map[string]any{"type": "admin", "id": "1001", "name": "Sam Lee", "email": "sam@northpeak.example",
			"app": map[string]any{"type": "app", "id_code": App, "name": "Northpeak", "region": "US"}})
	case r.Method == http.MethodGet && r.URL.Path == "/admins":
		out(map[string]any{"type": "admin.list", "admins": []any{
			map[string]any{"type": "admin", "id": "1001", "name": "Sam Lee", "email": "sam@northpeak.example"},
			map[string]any{"type": "admin", "id": "1002", "name": "Kim Park", "email": "kim@northpeak.example"}}})
	case r.Method == http.MethodGet && r.URL.Path == "/teams":
		out(map[string]any{"type": "team.list", "teams": []any{map[string]any{"type": "team", "id": "7", "name": "Support"}}})
	case r.Method == http.MethodPost && r.URL.Path == "/conversations/search":
		s.Searches++
		var body struct {
			Query struct {
				Field    string `json:"field"`
				Operator string `json:"operator"`
				Value    int64  `json:"value"`
			} `json:"query"`
			Pagination struct {
				PerPage       int    `json:"per_page"`
				StartingAfter string `json:"starting_after"`
			} `json:"pagination"`
			Sort struct {
				Field string `json:"field"`
				Order string `json:"order"`
			} `json:"sort"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Query.Field != "updated_at" || body.Query.Operator != ">" || body.Sort.Order != "ascending" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var list []map[string]any
		for _, cv := range s.conversations {
			if cv["updated_at"].(int64) > body.Query.Value {
				list = append(list, cv)
			}
		}
		slices.SortFunc(list, func(a, b map[string]any) int {
			if d := a["updated_at"].(int64) - b["updated_at"].(int64); d != 0 {
				return int(d)
			}
			return strings.Compare(a["id"].(string), b["id"].(string))
		})
		start, _ := strconv.Atoi(body.Pagination.StartingAfter)
		per := max(body.Pagination.PerPage, 1)
		end := min(len(list), start+per)
		page := []any{}
		for _, cv := range list[min(start, len(list)):end] {
			// Search results carry no parts.
			page = append(page, map[string]any{"type": "conversation", "id": cv["id"], "updated_at": cv["updated_at"]})
		}
		pages := map[string]any{"type": "pages", "per_page": per}
		if end < len(list) {
			pages["next"] = map[string]any{"starting_after": strconv.Itoa(end)}
		}
		out(map[string]any{"type": "conversation.list", "conversations": page, "total_count": len(list), "pages": pages})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/conversations/"):
		id := strings.TrimPrefix(r.URL.Path, "/conversations/")
		cv, ok := s.conversations[id]
		if !ok || r.URL.Query().Get("display_as") != "plaintext" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.Gets[id]++
		out(cv)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/contacts/"):
		ct, ok := s.contacts[strings.TrimPrefix(r.URL.Path, "/contacts/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		out(ct)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}
