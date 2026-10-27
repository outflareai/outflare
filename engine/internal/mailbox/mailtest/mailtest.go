// Package mailtest runs throwaway SMTP and IMAP servers with a self-signed
// certificate, so connection checks and sends can be tested without a real
// mailbox.
package mailtest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/outflareai/outflare/engine/internal/mailbox"
)

// Message is one message accepted by the SMTP server.
type Message struct {
	From string
	To   []string
	Data []byte
}

// Server is a pair of SMTP and IMAP servers on 127.0.0.1 that accept one
// username and password. Both offer STARTTLS on a random port.
type Server struct {
	Host     string
	SMTPPort int
	IMAPPort int
	// ClientTLS trusts the servers' certificate.
	ClientTLS *tls.Config

	mu       sync.Mutex
	users    map[string]string
	messages []Message
	reject   map[string]bool
	inboxes  map[string]*imapmemserver.User
	tokens   map[[2]string]bool // OAuth access token and username
	local    bool
	spam     map[string]bool // senders whose mail lands in Junk
}

// DeliverLocally puts mail sent to one of the server's own users in that
// user's INBOX, as a real provider would.
func (s *Server) DeliverLocally() {
	s.mu.Lock()
	s.local = true
	s.mu.Unlock()
}

// SpamFrom files mail from a sender in the recipient's Junk folder when it
// is delivered locally.
func (s *Server) SpamFrom(addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.spam == nil {
		s.spam = map[string]bool{}
	}
	s.spam[strings.ToLower(addr)] = true
}

// Count is how many messages a user has in a folder.
func (s *Server) Count(t testing.TB, user, folder string) int {
	t.Helper()
	data, err := s.inboxes[user].Status(folder, &imap.StatusOptions{NumMessages: true})
	if err != nil {
		t.Fatal(err)
	}
	return int(*data.NumMessages)
}

// AllowToken lets token sign in as user with XOAUTH2, as Gmail and
// Exchange Online accept.
func (s *Server) AllowToken(user, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokens == nil {
		s.tokens = map[[2]string]bool{}
	}
	s.tokens[[2]string{token, strings.ToLower(user)}] = true
}

// xoauth2 checks a SASL XOAUTH2 response and calls ok with the user.
type xoauth2 struct {
	srv  *Server
	ok   func(user string) error
	fail func() error
	sent bool
}

func (x *xoauth2) Next(resp []byte) ([]byte, bool, error) {
	if x.sent {
		// The client acknowledged the error challenge.
		return nil, true, x.fail()
	}
	if resp == nil {
		return []byte{}, false, nil // ask for the initial response
	}
	var user, token string
	for _, f := range strings.Split(string(resp), "\x01") {
		if v, ok := strings.CutPrefix(f, "user="); ok {
			user = v
		} else if v, ok := strings.CutPrefix(f, "auth=Bearer "); ok {
			token = v
		}
	}
	x.srv.mu.Lock()
	ok := x.srv.tokens[[2]string{token, strings.ToLower(user)}]
	x.srv.mu.Unlock()
	if ok {
		return nil, true, x.ok(user)
	}
	// Gmail answers a bad token with a JSON error challenge first.
	x.sent = true
	return []byte(`{"status":"401","schemes":"Bearer","scope":"https://mail.google.com/"}`), false, nil
}

// Deliver puts a raw message in a user's INBOX, as if it had been received.
func (s *Server) Deliver(t testing.TB, user string, raw string) {
	t.Helper()
	s.DeliverAt(t, user, raw, time.Now())
}

// DeliverAt is Deliver with the time the server received it.
func (s *Server) DeliverAt(t testing.TB, user string, raw string, at time.Time) {
	t.Helper()
	u := s.inboxes[user]
	if u == nil {
		t.Fatalf("mailtest: no user %s", user)
	}
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\n", "\r\n")
	if _, err := u.Append("INBOX", literal{strings.NewReader(raw), int64(len(raw))}, &imap.AppendOptions{Time: at}); err != nil {
		t.Fatal(err)
	}
}

type literal struct {
	*strings.Reader
	n int64
}

func (l literal) Size() int64 { return l.n }

// Reject makes the SMTP server refuse an address at RCPT with a 550, like a
// mailbox that does not exist.
func (s *Server) Reject(addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reject == nil {
		s.reject = map[string]bool{}
	}
	s.reject[strings.ToLower(addr)] = true
}

// Start runs both servers until the test ends. users maps username to password.
func Start(t testing.TB, users map[string]string) *Server {
	t.Helper()
	cert, pool := selfSigned(t)
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}}
	s := &Server{Host: "127.0.0.1", ClientTLS: &tls.Config{RootCAs: pool}, users: users}

	// SMTP
	ss := smtp.NewServer(smtp.BackendFunc(func(*smtp.Conn) (smtp.Session, error) {
		return &smtpSession{srv: s}, nil
	}))
	ss.Domain = "localhost"
	ss.TLSConfig = serverTLS
	ss.ErrorLog = log.New(io.Discard, "", 0)
	sl := listen(t)
	go ss.Serve(sl) //nolint:errcheck
	t.Cleanup(func() { ss.Close() })
	s.SMTPPort = sl.Addr().(*net.TCPAddr).Port

	// IMAP
	mem := imapmemserver.New()
	s.inboxes = map[string]*imapmemserver.User{}
	for u, p := range users {
		user := imapmemserver.NewUser(u, p)
		user.Create("INBOX", nil) //nolint:errcheck
		user.Create("Junk", nil)  //nolint:errcheck
		mem.AddUser(user)
		s.inboxes[u] = user
	}
	is := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return &imapSession{mem.NewSession().(imapserver.SessionIMAP4rev2), s}, nil, nil
		},
		Caps:      imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		TLSConfig: serverTLS,
		Logger:    log.New(io.Discard, "", 0),
	})
	il := listen(t)
	go is.Serve(il) //nolint:errcheck
	t.Cleanup(func() { is.Close() })
	s.IMAPPort = il.Addr().(*net.TCPAddr).Port
	return s
}

// imapSession adds XOAUTH2 to the in-memory server's session.
type imapSession struct {
	imapserver.SessionIMAP4rev2
	srv *Server
}

func (s *imapSession) AuthenticateMechanisms() []string { return []string{sasl.Plain, "XOAUTH2"} }

func (s *imapSession) Authenticate(mech string) (sasl.Server, error) {
	switch mech {
	case sasl.Plain:
		return sasl.NewPlainServer(func(_, user, pass string) error { return s.Login(user, pass) }), nil
	case "XOAUTH2":
		return &xoauth2{srv: s.srv,
			ok: func(user string) error {
				s.srv.mu.Lock()
				pass := s.srv.users[user]
				s.srv.mu.Unlock()
				return s.Login(user, pass)
			},
			fail: func() error {
				return &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeAuthenticationFailed, Text: "Invalid credentials (Failure)"}
			}}, nil
	}
	return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Text: "SASL mechanism not supported"}
}

// Messages returns what the SMTP server has accepted so far.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.messages...)
}

// Route sends SMTP and IMAP connections to these servers, whatever host
// and port the mailbox names. Use it as mailbox.Dialer.Route.
func (s *Server) Route(ep mailbox.Endpoint) mailbox.Endpoint {
	if ep.Port == 465 || ep.Port == 587 || ep.Port == 25 {
		return mailbox.Endpoint{Host: s.Host, Port: s.SMTPPort}
	}
	return mailbox.Endpoint{Host: s.Host, Port: s.IMAPPort}
}

// Addr is host:port for a port.
func (s *Server) Addr(port int) string { return net.JoinHostPort(s.Host, strconv.Itoa(port)) }

func listen(t testing.TB) net.Listener {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

type smtpSession struct {
	srv  *Server
	auth bool
	msg  Message
}

func (s *smtpSession) AuthMechanisms() []string { return []string{sasl.Plain, "XOAUTH2"} }

func (s *smtpSession) Auth(mech string) (sasl.Server, error) {
	if mech == "XOAUTH2" {
		return &xoauth2{srv: s.srv,
			ok: func(string) error { s.auth = true; return nil },
			fail: func() error {
				return &smtp.SMTPError{Code: 535, EnhancedCode: smtp.EnhancedCode{5, 7, 8}, Message: "Username and Password not accepted"}
			}}, nil
	}
	return sasl.NewPlainServer(func(_, user, pass string) error {
		s.srv.mu.Lock()
		want, ok := s.srv.users[user]
		s.srv.mu.Unlock()
		if !ok || want != pass {
			return &smtp.SMTPError{Code: 535, EnhancedCode: smtp.EnhancedCode{5, 7, 8}, Message: "Username and Password not accepted"}
		}
		s.auth = true
		return nil
	}), nil
}

func (s *smtpSession) Mail(from string, _ *smtp.MailOptions) error {
	if !s.auth {
		return smtp.ErrAuthRequired
	}
	s.msg = Message{From: from}
	return nil
}

func (s *smtpSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	s.srv.mu.Lock()
	reject := s.srv.reject[strings.ToLower(to)]
	s.srv.mu.Unlock()
	if reject {
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "No such user"}
	}
	s.msg.To = append(s.msg.To, to)
	return nil
}

func (s *smtpSession) Data(r io.Reader) error {
	var b bytes.Buffer
	if _, err := io.Copy(&b, r); err != nil {
		return err
	}
	s.msg.Data = b.Bytes()
	s.srv.mu.Lock()
	s.srv.messages = append(s.srv.messages, s.msg)
	local, spam := s.srv.local, s.srv.spam[strings.ToLower(s.msg.From)]
	s.srv.mu.Unlock()
	if local {
		folder := "INBOX"
		if spam {
			folder = "Junk"
		}
		raw := string(s.msg.Data)
		for _, to := range s.msg.To {
			if u := s.srv.inboxes[to]; u != nil {
				if _, err := u.Append(folder, literal{strings.NewReader(raw), int64(len(raw))}, &imap.AppendOptions{Time: time.Now()}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *smtpSession) Reset()        { s.msg = Message{} }
func (s *smtpSession) Logout() error { return nil }

func selfSigned(t testing.TB) (tls.Certificate, *x509.CertPool) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mailtest"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}
