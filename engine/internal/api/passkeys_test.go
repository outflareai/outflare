package api_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"github.com/outflareai/outflare/engine/internal/api"
)

// authenticator is a software passkey, like a phone or a security key, for
// the relying party localhost (the tests' PUBLIC_URL).
type authenticator struct {
	key    *ecdsa.PrivateKey
	id     []byte
	user   []byte // the user handle it stored at registration
	count  uint32
	origin string
}

func newAuthenticator(t *testing.T) *authenticator {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	rand.Read(id) //nolint:errcheck
	return &authenticator{key: key, id: id, origin: "http://localhost:8080"}
}

var b64 = base64.RawURLEncoding

// Flags: user present, user verified, backup eligible and backed up (a
// synced passkey), and for registration, attested credential data.
const (
	flagUP, flagUV, flagBE, flagBS, flagAT = 0x01, 0x04, 0x08, 0x10, 0x40
)

func (a *authenticator) authData(flags byte, attested []byte) []byte {
	rp := sha256.Sum256([]byte("localhost"))
	var buf bytes.Buffer
	buf.Write(rp[:])
	buf.WriteByte(flags)
	binary.Write(&buf, binary.BigEndian, a.count) //nolint:errcheck
	buf.Write(attested)
	return buf.Bytes()
}

func clientData(typ string, options map[string]any, origin string) []byte {
	pk := options["publicKey"].(map[string]any)
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": pk["challenge"], "origin": origin, "crossOrigin": false})
	return b
}

// create answers navigator.credentials.create with options from the server.
func (a *authenticator) create(t *testing.T, options map[string]any) map[string]any {
	t.Helper()
	user, err := b64.DecodeString(options["publicKey"].(map[string]any)["user"].(map[string]any)["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	a.user = user
	x, y := a.key.PublicKey.X.FillBytes(make([]byte, 32)), a.key.PublicKey.Y.FillBytes(make([]byte, 32))
	cose, _ := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	var att bytes.Buffer
	att.Write(make([]byte, 16))                             // AAGUID
	binary.Write(&att, binary.BigEndian, uint16(len(a.id))) //nolint:errcheck
	att.Write(a.id)
	att.Write(cose)
	obj, _ := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": a.authData(flagUP|flagUV|flagBE|flagBS|flagAT, att.Bytes())})
	return map[string]any{
		"id": b64.EncodeToString(a.id), "rawId": b64.EncodeToString(a.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(clientData("webauthn.create", options, a.origin)),
			"attestationObject": b64.EncodeToString(obj),
			"transports":        []string{"internal"},
		},
		"clientExtensionResults": map[string]any{},
	}
}

// get answers navigator.credentials.get.
func (a *authenticator) get(t *testing.T, options map[string]any) map[string]any {
	t.Helper()
	a.count++
	data := a.authData(flagUP|flagUV|flagBE|flagBS, nil)
	cd := clientData("webauthn.get", options, a.origin)
	h := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, data...), h[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"id": b64.EncodeToString(a.id), "rawId": b64.EncodeToString(a.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(cd),
			"authenticatorData": b64.EncodeToString(data),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        b64.EncodeToString(a.user),
		},
		"clientExtensionResults": map[string]any{},
	}
}

// browser keeps every cookie, as the passkey ceremony needs its own.
type browser struct {
	t       *testing.T
	h       http.Handler
	cookies map[string]*http.Cookie
}

func (b *browser) do(method, path string, body any) (int, map[string]any) {
	b.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range b.cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	b.h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c
		}
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out) //nolint:errcheck
	return rec.Code, out
}

func (b *browser) must(want int, method, path string, body any) map[string]any {
	b.t.Helper()
	code, out := b.do(method, path, body)
	if code != want {
		b.t.Fatalf("%s %s: status %d, want %d: %v", method, path, code, want, out)
	}
	return out
}

func TestPasskeys(t *testing.T) {
	e := freshEnv(t)
	// Off unless the server is told its address is final.
	off := &browser{t: t, h: e.h, cookies: map[string]*http.Cookie{}}
	off.must(404, "POST", "/v1/auth/passkey/options", nil)

	h := handler(t, api.NewServer(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)), api.Options{Box: e.box, Mailer: e.outbox, Passkeys: true}))
	jack := &browser{t: t, h: h, cookies: map[string]*http.Cookie{}}
	jack.must(201, "POST", "/v1/setup", map[string]string{"email": "jack@example.com", "name": "Jack", "password": "long enough password", "workspace_name": "Acme"})
	if m := jack.must(200, "GET", "/v1/meta", nil)["login_methods"].([]any); m[len(m)-1] != "passkey" {
		t.Fatalf("login methods: %v", m)
	}

	// Add a passkey.
	phone := newAuthenticator(t)
	opts := jack.must(200, "POST", "/v1/me/passkeys/options", nil)
	if rp := opts["publicKey"].(map[string]any)["rp"].(map[string]any)["id"]; rp != "localhost" {
		t.Fatalf("relying party: %v", rp)
	}
	pk := jack.must(201, "POST", "/v1/me/passkeys?name=Jack%27s%20iPhone", phone.create(t, opts))
	if pk["name"] != "Jack's iPhone" || pk["synced"] != true {
		t.Fatalf("passkey: %v", pk)
	}
	// The challenge works once.
	jack.must(400, "POST", "/v1/me/passkeys", phone.create(t, opts))
	// A response from another site is refused.
	phish := newAuthenticator(t)
	phish.origin = "https://outflare.example.evil"
	jack.must(400, "POST", "/v1/me/passkeys", phish.create(t, jack.must(200, "POST", "/v1/me/passkeys/options", nil)))
	list := jack.must(200, "GET", "/v1/me/passkeys", nil)["passkeys"].([]any)
	if len(list) != 1 {
		t.Fatalf("passkeys: %v", list)
	}

	// Sign in with it in a new browser, without an email.
	other := &browser{t: t, h: h, cookies: map[string]*http.Cookie{}}
	other.must(401, "GET", "/v1/me", nil)
	other.must(200, "POST", "/v1/auth/passkey", phone.get(t, other.must(200, "POST", "/v1/auth/passkey/options", nil)))
	if me := other.must(200, "GET", "/v1/me", nil)["user"].(map[string]any); me["email"] != "jack@example.com" {
		t.Fatalf("signed in as %v", me)
	}
	if list := jack.must(200, "GET", "/v1/me/passkeys", nil)["passkeys"].([]any); list[0].(map[string]any)["last_used_at"] == nil {
		t.Fatalf("last used not recorded: %v", list)
	}

	// A replayed answer (old counter, used challenge) fails.
	stale := phone.get(t, other.must(200, "POST", "/v1/auth/passkey/options", nil))
	other.must(200, "POST", "/v1/auth/passkey", stale)
	other.must(400, "POST", "/v1/auth/passkey", stale) // the challenge is gone

	// An unknown passkey is refused.
	stranger := newAuthenticator(t)
	stranger.user = []byte(pk["id"].(string)) // not a user id
	fresh := &browser{t: t, h: h, cookies: map[string]*http.Cookie{}}
	fresh.must(401, "POST", "/v1/auth/passkey", stranger.get(t, fresh.must(200, "POST", "/v1/auth/passkey/options", nil)))

	// Rename and remove; then it no longer signs in.
	id := pk["id"].(string)
	if out := jack.must(200, "PATCH", "/v1/me/passkeys/"+id, map[string]string{"name": "Work phone"}); out["name"] != "Work phone" {
		t.Fatalf("rename: %v", out)
	}
	jack.must(404, "DELETE", "/v1/me/passkeys/not-a-uuid", nil)
	jack.must(204, "DELETE", "/v1/me/passkeys/"+id, nil)
	gone := &browser{t: t, h: h, cookies: map[string]*http.Cookie{}}
	gone.must(401, "POST", "/v1/auth/passkey", phone.get(t, gone.must(200, "POST", "/v1/auth/passkey/options", nil)))
}
