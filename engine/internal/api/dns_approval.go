package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/domainconnect"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The one-off way to add a domain's records: the person approves them on
// their DNS provider's own screen (Domain Connect), and Outflare keeps no
// access afterwards. GET .../dns/approval says whether the domain's
// provider can, with the signed link; the provider sends the browser back
// to the page, which reports with POST .../dns/approved so the checks
// start. Where it can't, the page offers a DNS host connection instead
// (POST .../dns).

// dnsApprovalView answers GET .../dns/approval.
type dnsApprovalView struct {
	Available bool   `json:"available"`
	Provider  string `json:"provider,omitempty"`
	URL       string `json:"url,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// nothingToAdd is a lookup's outcome when every record is in place.
const nothingToAdd = "nothing_to_add"

// approvalErrors are the errors a DNS provider may send back (OAuth's).
var approvalErrors = []string{"invalid_request", "unauthorized_client", "access_denied", "unsupported_response_type", "invalid_scope", "server_error", "temporarily_unavailable"}

// dnsSubject is something whose records a person can approve.
type dnsSubject struct {
	Subject string // seo_site, tracking_domain, email_domain
	ID      string
	BrandID string
	Kind    string // the templates' kind: site, tracking, email
	FQDN    string // where the records go
	All     []domainconnect.Rec
	Want    []domainconnect.Rec // the ones not in place yet
}

// domainConnect gives the client with the server's signing key, loading
// (or making) the key on first use.
func (s *Server) domainConnect(ctx context.Context) *domainconnect.Client {
	s.dcMu.Lock()
	defer s.dcMu.Unlock()
	if s.dc.Key == nil && s.box != nil {
		k, err := domainconnect.LoadKey(ctx, s.pool, s.box)
		if err != nil {
			track.Fail(ctx, s.log, track.Event{Name: "domain_connect.key_failed", Surface: "system", ActorType: "system"}, "domain_connect_key", err)
			return s.dc
		}
		s.dc.Key = k
	}
	return s.dc
}

func (s *Server) dnsApproval(w http.ResponseWriter, r *http.Request, sub dnsSubject) {
	ctx := r.Context()
	ret := r.URL.Query().Get("return")
	if !strings.HasPrefix(ret, "/") || strings.HasPrefix(ret, "//") || strings.ContainsAny(ret, "\\\r\n") {
		ret = "/"
	}
	start := time.Now()
	v, err := s.approvalFor(ctx, sub, s.publicURL+ret)
	outcome := "available"
	if !v.Available {
		outcome = v.Reason
	}
	metrics.DomainConnectLookup(outcome, time.Since(start))
	if err != nil {
		track.Fail(ctx, s.log, track.Event{Name: sub.Subject + ".dns_approval_lookup_failed", WorkspaceID: accessFrom(ctx).WorkspaceID,
			Surface: "api", Props: map[string]any{"subject": sub.Subject}}, "domain_connect_lookup_failed", err)
	}
	writeJSON(w, http.StatusOK, v)
}

// approvalFor finds the provider and builds the link; err is set only for
// a lookup that failed, not for a provider that doesn't support it.
func (s *Server) approvalFor(ctx context.Context, sub dnsSubject, redirect string) (dnsApprovalView, error) {
	if len(sub.Want) == 0 {
		return dnsApprovalView{Reason: nothingToAdd}, nil
	}
	dc := s.domainConnect(ctx)
	if !dc.Ready(ctx) {
		return dnsApprovalView{Reason: domainconnect.ReasonNotConfigured}, nil
	}
	plan, ok := domainconnect.PlanFor(sub.Kind, sub.FQDN, sub.All, sub.Want)
	if !ok {
		return dnsApprovalView{Reason: domainconnect.ReasonNoTemplate}, nil
	}
	zone, err := dc.Discover(ctx, sub.FQDN)
	if err == nil {
		err = dc.Supports(ctx, zone.Settings, plan.Template.ServiceID)
	}
	if err != nil {
		v := dnsApprovalView{Reason: domainconnect.ReasonOf(err), Provider: zone.Settings.Name()}
		if v.Reason != domainconnect.ReasonLookupFailed {
			err = nil
		}
		return v, err
	}
	link, err := dc.ApplyURL(zone, plan, redirect, "dc."+sub.ID)
	if err != nil {
		return dnsApprovalView{Reason: domainconnect.ReasonLookupFailed}, err
	}
	return dnsApprovalView{Available: true, Provider: zone.Settings.Name(), URL: link}, nil
}

// dnsApproved records that the person came back from their DNS provider,
// and starts looking for the records. The records themselves are only
// trusted once DNS shows them.
func (s *Server) dnsApproved(w http.ResponseWriter, r *http.Request, a *access, sub dnsSubject) {
	var req struct {
		Error    string `json:"error"`
		Provider string `json:"provider"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Error != "" && !slices.Contains(approvalErrors, req.Error) {
		req.Error = "other"
	}
	provider := ""
	if len(req.Provider) <= 60 {
		provider = req.Provider
	}
	ctx := r.Context()
	if req.Error != "" {
		// Cancelled or refused at the provider: nothing changed here.
		outcome := track.Failure
		if req.Error == "server_error" || req.Error == "temporarily_unavailable" {
			outcome = track.Error
		}
		track.Emit(ctx, track.Event{Name: sub.Subject + ".dns_approval_failed", WorkspaceID: a.WorkspaceID, UserID: a.UserID, ActorID: a.ActorID,
			ActorType: a.ActorType, Surface: "api", Outcome: outcome, ErrorCode: "dns_approval_" + req.Error})
		writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
		return
	}
	run, _ := json.Marshal(connectors.DNSInstall{Status: "done", Provider: provider, At: time.Now().UTC()})
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		switch sub.Subject {
		case "email_domain":
			if _, err := tx.Exec(ctx, `UPDATE email_domains SET dns_install = $3, updated_at = now() WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, sub.ID, run); err != nil {
				return err
			}
			if err := s.emails.CheckDomain(ctx, tx, a.WorkspaceID, sub.ID); err != nil {
				return err
			}
		default:
			if _, err := tx.Exec(ctx, `UPDATE `+dnsSubjects[sub.Subject]+` SET dns_install = $3 WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, sub.ID, run); err != nil {
				return err
			}
			if err := s.afterDNSAdded(ctx, tx, a.WorkspaceID, dnsAddJob{Subject: sub.Subject, ID: sub.ID}); err != nil {
				return err
			}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, sub.Subject+".dns_approved", sub.Subject, sub.ID, map[string]any{"via": "domain_connect", "provider": provider})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

// approvalRecs turns records into what the templates match.
func approvalRecs(rs []connectors.DomainRecord) (all, want []domainconnect.Rec) {
	for _, r := range rs {
		rec := domainconnect.Rec{Type: strings.ToUpper(r.Type), Name: strings.TrimSuffix(r.Name, "."), Value: r.Value, Priority: r.Priority}
		all = append(all, rec)
		if r.Status != "verified" {
			want = append(want, rec)
		}
	}
	return all, want
}

// The subjects, each loaded with the caller's access checked.

func (s *Server) seoSiteSubject(w http.ResponseWriter, r *http.Request) (*access, dnsSubject, bool) {
	a := accessFrom(r.Context())
	site, ok := s.seoSiteFor(w, r, a)
	if !ok {
		return nil, dnsSubject{}, false
	}
	if !a.writes(nil, site.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return nil, dnsSubject{}, false
	}
	rec := domainconnect.Rec{Type: "TXT", Name: site.Verification.DNSName, Value: site.Verification.DNSValue}
	sub := dnsSubject{Subject: "seo_site", ID: site.ID, BrandID: deref(site.BrandID), Kind: "site", FQDN: site.Verification.DNSName, All: []domainconnect.Rec{rec}}
	if !site.Verified {
		sub.Want = sub.All
	}
	return a, sub, true
}

func (s *Server) trackingSubject(w http.ResponseWriter, r *http.Request) (*access, dnsSubject, bool) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	d, err := s.trackingDomain(r.Context(), a.WorkspaceID, id)
	if !validUUID(id) || errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "tracking domain not found")
		return nil, dnsSubject{}, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, dnsSubject{}, false
	}
	rec := domainconnect.Rec{Type: "CNAME", Name: d.Domain.Domain, Value: d.Target}
	sub := dnsSubject{Subject: "tracking_domain", ID: d.ID, Kind: "tracking", FQDN: d.Domain.Domain, All: []domainconnect.Rec{rec}}
	if d.Status != "verified" {
		sub.Want = sub.All
	}
	return a, sub, true
}

func (s *Server) emailDomainSubject(w http.ResponseWriter, r *http.Request) (*access, dnsSubject, bool) {
	a := accessFrom(r.Context())
	cur, ok := s.findEmailDomain(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return nil, dnsSubject{}, false
	}
	sub := dnsSubject{Subject: "email_domain", ID: cur.ID, BrandID: deref(cur.BrandID), Kind: "email", FQDN: cur.Domain}
	sub.All, sub.Want = approvalRecs(cur.Records)
	return a, sub, true
}

func (s *Server) seoSiteDNSApproval(w http.ResponseWriter, r *http.Request) {
	if _, sub, ok := s.seoSiteSubject(w, r); ok {
		s.dnsApproval(w, r, sub)
	}
}

func (s *Server) seoSiteDNSApproved(w http.ResponseWriter, r *http.Request) {
	if a, sub, ok := s.seoSiteSubject(w, r); ok {
		s.dnsApproved(w, r, a, sub)
	}
}

func (s *Server) trackingDomainDNSApproval(w http.ResponseWriter, r *http.Request) {
	if _, sub, ok := s.trackingSubject(w, r); ok {
		s.dnsApproval(w, r, sub)
	}
}

func (s *Server) trackingDomainDNSApproved(w http.ResponseWriter, r *http.Request) {
	if a, sub, ok := s.trackingSubject(w, r); ok {
		s.dnsApproved(w, r, a, sub)
	}
}

func (s *Server) emailDomainDNSApproval(w http.ResponseWriter, r *http.Request) {
	if _, sub, ok := s.emailDomainSubject(w, r); ok {
		s.dnsApproval(w, r, sub)
	}
}

func (s *Server) emailDomainDNSApproved(w http.ResponseWriter, r *http.Request) {
	if a, sub, ok := s.emailDomainSubject(w, r); ok {
		s.dnsApproved(w, r, a, sub)
	}
}

// domainConnectKey is GET /v1/domain-connect: the public key to publish
// at <key>.outflare.ai and the templates this server applies. Public: it
// holds nothing secret.
func (s *Server) domainConnectKey(w http.ResponseWriter, r *http.Request) {
	dc := s.domainConnect(r.Context())
	out := map[string]any{"provider_id": domainconnect.ProviderID, "ready": dc.Ready(r.Context()), "key": nil}
	var ids []string
	for _, t := range domainconnect.Templates {
		ids = append(ids, t.ServiceID)
	}
	out["templates"] = ids
	if dc.Key != nil {
		out["key"] = map[string]any{"host": dc.Key.ID + "." + domainconnect.PubKeyDomain, "txt": dc.Key.TXT()}
	}
	writeJSON(w, http.StatusOK, out)
}
