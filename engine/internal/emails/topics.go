package emails

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/policy"
)

// Subscription topics. What a person chose is a consent on their customer
// record (channel email, topic = the topic's key, purpose marketing), so
// the CRM, journeys and the API all see the same answer. Transactional
// email ignores topics and consents: people asked for it.

// Topic is a lifecycle email topic as the preference page shows it.
type Topic struct {
	ID, Key, Name, Description string
	SubscribedByDefault        bool
	Public                     bool
	// Subscribed is this person's answer, or the default when they never
	// gave one.
	Subscribed bool
}

// subscribed applies a consent to a topic's default.
func subscribed(status string, byDefault bool) bool {
	switch status {
	case customers.Granted:
		return true
	case customers.Withdrawn:
		return false
	}
	return byDefault
}

// topicBlocks returns the recipients a lifecycle email must not go to
// because of what they chose, with why: they withdrew from its topic or
// from all marketing email, the topic is opt-in and they never opted in,
// or their contact basis rules it out (see skipFor). Someone with no
// customer record has never chosen, and nothing says how the workspace
// got them.
func topicBlocks(ctx context.Context, tx pgx.Tx, ws, purpose string, topicID *string, recipients []string) (map[string]string, error) {
	out := map[string]string{}
	if purpose != policy.PurposeLifecycle {
		return out, nil
	}
	key, name, byDefault := "", "", true
	if topicID != nil {
		err := tx.QueryRow(ctx, `SELECT key, name, subscribed_by_default FROM email_topics WHERE workspace_id = $1 AND id = $2`, ws, *topicID).
			Scan(&key, &name, &byDefault)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	for _, addr := range recipients {
		addr = strings.ToLower(addr)
		rec, ok, err := customers.OwnerOf(ctx, tx, ws, "email", addr)
		if err != nil {
			return nil, err
		}
		why := ""
		if ok {
			var origin string
			if err := tx.QueryRow(ctx, `SELECT origin FROM records WHERE workspace_id = $1 AND id = $2`, ws, rec).Scan(&origin); err != nil {
				return nil, err
			}
			why, err = skipFor(ctx, tx, ws, rec, origin, key, name, byDefault)
		} else if why = choiceBlock("", key, name, byDefault); why == "" {
			why, err = basisBlock(ctx, tx, ws, "", "", false)
		}
		if err != nil {
			return nil, err
		}
		if why != "" {
			out[addr] = why
		}
	}
	return out, nil
}

// choiceBlock is why someone's consent keeps a lifecycle email of a topic
// (key "" for none) from them, or "".
func choiceBlock(status, key, name string, byDefault bool) string {
	switch {
	case status == customers.Withdrawn && key != "":
		return "unsubscribed from " + name
	case status == customers.Withdrawn:
		return "unsubscribed from marketing email"
	case !subscribed(status, byDefault):
		return "not subscribed to " + name + ", which people opt in to"
	}
	return ""
}

// Preferences is what the preference page behind an email's unsubscribe
// link shows.
type Preferences struct {
	WorkspaceID, Address string
	// Who sends it: the workspace's name.
	Sender string
	// The email's own topic, if it has one.
	TopicKey, TopicName string
	Topics              []Topic
	// AllWithdrawn: they unsubscribed from all marketing email.
	AllWithdrawn bool
}

// Preferences loads the choices of the first recipient of an email.
func (s *Service) Preferences(ctx context.Context, emailID string) (*Preferences, error) {
	var p Preferences
	var to []string
	var topicKey, topicName *string
	err := s.Pool.QueryRow(ctx, `SELECT e.workspace_id, w.name, e.to_addresses, tp.key, tp.name FROM emails e
		JOIN workspaces w ON w.id = e.workspace_id
		LEFT JOIN email_topics tp ON tp.id = e.topic_id WHERE e.id = $1`, emailID).Scan(&p.WorkspaceID, &p.Sender, &to, &topicKey, &topicName)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && len(to) == 0) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Address = strings.ToLower(to[0])
	if topicKey != nil {
		p.TopicKey, p.TopicName = *topicKey, *topicName
	}
	rec, ok, err := customers.OwnerOf(ctx, s.Pool, p.WorkspaceID, "email", p.Address)
	if err != nil {
		return nil, err
	}
	consent := func(key string) (string, error) {
		if !ok {
			return "", nil
		}
		st, _, err := customers.Consent(ctx, s.Pool, p.WorkspaceID, rec, "email", key, "marketing")
		return st, err
	}
	all, err := consent("")
	if err != nil {
		return nil, err
	}
	p.AllWithdrawn = all == customers.Withdrawn
	rows, err := s.Pool.Query(ctx, `SELECT id, key, name, description, subscribed_by_default, visibility = 'public'
		FROM email_topics WHERE workspace_id = $1 AND status = 'active' ORDER BY name, key`, p.WorkspaceID)
	if err != nil {
		return nil, err
	}
	topics, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Topic, error) {
		var t Topic
		err := row.Scan(&t.ID, &t.Key, &t.Name, &t.Description, &t.SubscribedByDefault, &t.Public)
		return t, err
	})
	if err != nil {
		return nil, err
	}
	for _, t := range topics {
		st, err := consent(t.Key)
		if err != nil {
			return nil, err
		}
		t.Subscribed = subscribed(st, t.SubscribedByDefault) && !p.AllWithdrawn
		// A private topic shows only to people who get it, and the
		// email's own topic always shows.
		if t.Public || t.Subscribed || t.Key == p.TopicKey {
			p.Topics = append(p.Topics, t)
		}
	}
	return &p, nil
}

// PreferenceChange is what someone saved on the preference page: the
// topics they want (by key, out of the ones shown), or all marketing email
// off.
type PreferenceChange struct {
	Subscribed map[string]bool
	All        bool
	// unsubscribe_link (the email's link or their mail app's button) or
	// preference_center (the page).
	Source    string
	RequestID string
}

// SetPreferences records what the first recipient of an email chose, as
// consents on their customer record (made if they have none: following a
// link to their inbox shows they own the address).
func (s *Service) SetPreferences(ctx context.Context, emailID string, c PreferenceChange) error {
	p, err := s.Preferences(ctx, emailID)
	if err != nil || p == nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		rec, err := customers.EnsurePerson(ctx, tx, p.WorkspaceID, "email", p.Address, true, c.RequestID)
		if err != nil {
			return err
		}
		evidence := map[string]any{"email_id": emailID}
		write := func(key, status string) error {
			return customers.RecordConsent(ctx, tx, p.WorkspaceID, customers.ConsentChange{RecordID: rec, Channel: "email", Topic: key,
				Status: status, Source: c.Source, Evidence: evidence, RequestID: c.RequestID})
		}
		changed := 0
		if c.All != p.AllWithdrawn {
			status := customers.Granted
			if c.All {
				status = customers.Withdrawn
			}
			if err := write("", status); err != nil {
				return err
			}
			changed++
		}
		if !c.All {
			for _, t := range p.Topics {
				want, ok := c.Subscribed[t.Key]
				// Coming back from all-off shows every topic off; only
				// what they tick turns on.
				if !ok || (want == t.Subscribed && !p.AllWithdrawn) {
					continue
				}
				status := customers.Withdrawn
				if want {
					status = customers.Granted
				}
				if err := write(t.Key, status); err != nil {
					return err
				}
				changed++
			}
		}
		if changed == 0 {
			return nil
		}
		if err := addEvent(ctx, tx, p.WorkspaceID, emailID, "preferences_changed", p.Address, "", ""); err != nil {
			return err
		}
		return s.record(ctx, tx, System, p.WorkspaceID, "email.preferences_changed", "email", emailID,
			map[string]any{"source": c.Source, "all_withdrawn": c.All, "changes": changed})
	})
}

// unsubscribeTopic withdraws an email's recipients from its topic: one
// click on the link, or their mail app's unsubscribe button.
func (s *Service) unsubscribeTopic(ctx context.Context, tx pgx.Tx, ws, emailID, key string, to []string, requestID string) (bool, error) {
	changed := false
	for _, a := range to {
		a = strings.ToLower(a)
		rec, err := customers.EnsurePerson(ctx, tx, ws, "email", a, true, requestID)
		if err != nil {
			return false, err
		}
		if st, _, err := customers.Consent(ctx, tx, ws, rec, "email", key, "marketing"); err != nil {
			return false, err
		} else if st == customers.Withdrawn {
			continue
		}
		if err := customers.RecordConsent(ctx, tx, ws, customers.ConsentChange{RecordID: rec, Channel: "email", Topic: key,
			Status: customers.Withdrawn, Source: "unsubscribe_link", Evidence: map[string]any{"email_id": emailID}, RequestID: requestID}); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}
