package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Signing a connection in to an account through the workspace's own app
// (connectors.AccountSignIn), such as Reddit: the connection already holds
// the app's id and secret, and the person signs in to the account the app
// should act as. The provider sends the browser back to the usual
// callback, which is the redirect URI the person registered on their app.

// integrationSignIn sends the browser to the provider's consent page for
// the connection's own app. Agents can read the Location header, or ask
// for ?format=json, and hand the link to a person.
func (s *Server) integrationSignIn(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "integration not found")
		return
	}
	o, err := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, id)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "integration not found")
		return
	}
	if err != nil {
		if connectors.KindOf(err) != "" {
			connectorError(w, r, err)
		} else {
			s.internal(w, r, err)
		}
		return
	}
	signer, ok := o.Connector.(connectors.AccountSignIn)
	o1, isOAuth1 := o.Connector.(connectors.AccountSignInOAuth1)
	if !ok && !isOAuth1 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", o.Connector.Definition().Name+" has no account sign-in; connect it from Connectors")
		return
	}
	back := r.URL.Query().Get("return")
	if !validReturn(back) {
		back = ""
	}
	state := integrationState{
		Workspace: a.WorkspaceID, Actor: a.ActorID, ActorType: a.ActorType, Connector: o.Connector.Definition().ID, Integration: id,
		Return: back, Verifier: newID() + newID(), Expires: time.Now().Add(15 * time.Minute).Unix(),
	}
	st, _ := json.Marshal(state)
	sealed := base64.RawURLEncoding.EncodeToString(s.box.Seal(st, []byte(integrationStatePurpose)))
	if isOAuth1 {
		s.integrationSignInOAuth1(w, r, o, o1, sealed)
		return
	}
	link := signer.SignInURL(o.Client, o.Creds, s.integrationRedirectURI(), sealed)
	if pk, ok := signer.(connectors.AccountSignInPKCE); ok {
		link = pk.SignInURLPKCE(o.Client, o.Creds, s.integrationRedirectURI(), sealed, state.Verifier)
	}
	if r.URL.Query().Get("format") == "json" {
		writeJSON(w, http.StatusOK, map[string]string{"url": link, "redirect_uri": s.integrationRedirectURI()})
		return
	}
	http.Redirect(w, r, link, http.StatusFound)
}

// oauth1StateCookie carries an OAuth 1.0a sign-in's sealed state through
// the provider. X matches callback URLs exactly, so the state can't ride in
// the callback's query string as it does for OAuth 2.0; the browser that
// started the sign-in brings it back instead.
const oauth1StateCookie = "outflare_oauth1_state"

// integrationSignInOAuth1 starts an OAuth 1.0a sign-in (X Ads): the
// provider hands out a request token for the plain callback URL, its secret
// is kept on the connection (sealed with its other credentials) until the
// person comes back, and the sealed state goes in a short-lived cookie.
func (s *Server) integrationSignInOAuth1(w http.ResponseWriter, r *http.Request, o *connectors.Opened, o1 connectors.AccountSignInOAuth1, sealed string) {
	ctx := r.Context()
	a := accessFrom(ctx)
	link, tok, sec, err := o1.SignInRequest(ctx, o.Client, o.Creds, s.integrationRedirectURI())
	if err != nil {
		connectorError(w, r, err)
		return
	}
	creds := connectors.Creds{}
	for k, v := range o.Creds {
		creds[k] = v
	}
	creds[connectors.OAuth1RequestToken], creds[connectors.OAuth1RequestSecret] = tok, sec
	if err := s.integrations.SetCreds(ctx, s.pool, a.WorkspaceID, o.ID, "", creds); err != nil {
		s.internal(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oauth1StateCookie, Value: sealed, Path: "/v1/integrations/oauth/callback",
		MaxAge: 15 * 60, HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteLaxMode})
	if r.URL.Query().Get("format") == "json" {
		writeJSON(w, http.StatusOK, map[string]string{"url": link, "redirect_uri": s.integrationRedirectURI()})
		return
	}
	http.Redirect(w, r, link, http.StatusFound)
}

// validReturn is an app page a sign-in may come back to.
func validReturn(p string) bool {
	return p == "get-started" || p == "setup" || p == "reddit" || p == "social" || p == "providers" || p == "audits" || p == "domains" || p == "ads"
}

// finishAccountSignIn completes integrationSignIn: it trades the code for
// the account's tokens with the connection's own app and stores them on
// the connection.
func (s *Server) finishAccountSignIn(w http.ResponseWriter, r *http.Request, st integrationState, done func(url.Values), fail func(string)) {
	ctx := r.Context()
	o, err := s.integrations.Open(ctx, s.pool, st.Workspace, st.Integration)
	if err != nil {
		fail("the connection is no longer there; connect it again")
		return
	}
	signer, ok := o.Connector.(connectors.AccountSignIn)
	o1, isOAuth1 := o.Connector.(connectors.AccountSignInOAuth1)
	if (!ok && !isOAuth1) || o.Connector.Definition().ID != st.Connector {
		fail("this connection cannot sign in to an account")
		return
	}
	var creds connectors.Creds
	var account connectors.Account
	if isOAuth1 {
		q := r.URL.Query()
		if q.Get("denied") != "" {
			fail("access was not granted")
			return
		}
		tok := q.Get("oauth_token")
		if tok == "" || tok != o.Creds[connectors.OAuth1RequestToken] {
			fail("the sign-in does not match the one started here; start again")
			return
		}
		creds, account, err = o1.SignInAccess(ctx, o.Client, o.Creds, tok, o.Creds[connectors.OAuth1RequestSecret], q.Get("oauth_verifier"))
	} else if pk, ok := signer.(connectors.AccountSignInPKCE); ok {
		creds, account, err = pk.SignInPKCE(ctx, o.Client, o.Creds, r.URL.Query().Get("code"), s.integrationRedirectURI(), st.Verifier)
	} else {
		creds, account, err = signer.SignIn(ctx, o.Client, o.Creds, r.URL.Query().Get("code"), s.integrationRedirectURI())
	}
	if err != nil {
		fail(truncate(err.Error(), 300))
		return
	}
	actorType := st.ActorType
	if actorType == "" {
		actorType = "human"
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.integrations.SetCreds(ctx, tx, st.Workspace, st.Integration, "", creds); err != nil {
			return err
		}
		if err := s.integrations.Checked(ctx, tx, st.Workspace, st.Integration, &account, nil); err != nil {
			return err
		}
		i, err := s.integrations.Get(ctx, tx, st.Workspace, st.Integration)
		if err != nil {
			return err
		}
		if err := s.integrations.Reconnected(ctx, tx, i); err != nil {
			return err
		}
		if o.Connector.Definition().Has(connectors.CapSocialMetrics) {
			// Find the accounts it now reaches and read them straight away.
			if err := enqueueSocialSync(ctx, tx, st.Workspace, st.Integration, "signin:"+newID()); err != nil {
				return err
			}
		}
		return record(ctx, tx, &principal{ActorID: st.Actor, ActorType: actorType}, st.Workspace, "integration.signed_in", "integration", st.Integration,
			map[string]any{"connector": st.Connector})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	done(url.Values{"integration": {st.Integration}, "signed_in": {"1"}})
}
