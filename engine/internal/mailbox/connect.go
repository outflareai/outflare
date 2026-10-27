package mailbox

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"syscall"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

// Dialer opens authenticated SMTP and IMAP sessions.
type Dialer struct {
	// TLSConfig is cloned per connection with ServerName set. Nil uses the
	// system roots. Tests set RootCAs here.
	TLSConfig *tls.Config
	// Timeout bounds the whole operation. Default 20s.
	Timeout time.Duration
	// Route, when set, changes where a connection goes. Tests use it to
	// send provider hosts such as smtp.gmail.com to a local server.
	Route func(Endpoint) Endpoint
	// Control, when set, vets each address before it is dialled; the
	// server uses netguard.Control so a mailbox's server must be public.
	Control func(network, address string, c syscall.RawConn) error
}

// Credentials for one protocol.
type Credentials struct {
	Username string
	Password string
	// Token is an OAuth access token. When set, sign-in uses XOAUTH2
	// instead of the password.
	Token string
}

// xoauth2 is the SASL XOAUTH2 mechanism used by Gmail and Exchange Online.
type xoauth2 struct{ user, token string }

func (x xoauth2) Start() (string, []byte, error) {
	return "XOAUTH2", []byte("user=" + x.user + "\x01auth=Bearer " + x.token + "\x01\x01"), nil
}

// Next answers the server's error challenge with an empty response, after
// which the server reports the failure.
func (x xoauth2) Next([]byte) ([]byte, error) { return []byte{}, nil }

// Stage says where a connection failed, so the message can tell the user
// what to fix.
type Stage string

const (
	StageConnect Stage = "connect"
	StageTLS     Stage = "tls"
	StageAuth    Stage = "auth"
	StageMailbox Stage = "mailbox"
	StageSend    Stage = "send"
	// StageRcpt is the server refusing a recipient; a permanent (5xx)
	// refusal here is a hard bounce.
	StageRcpt Stage = "recipient"
)

// ConnError is a failed connection step.
type ConnError struct {
	Protocol string // SMTP or IMAP
	Stage    Stage
	Err      error
}

func (e *ConnError) Error() string {
	hint := ""
	switch e.Stage {
	case StageConnect:
		hint = "could not reach the server; check the host and port"
	case StageTLS:
		hint = "secure connection failed"
	case StageAuth:
		hint = "sign-in rejected; check the username and app password, and that " + e.Protocol + " access is enabled for this mailbox"
	case StageMailbox:
		hint = "signed in but could not open the inbox"
	case StageSend:
		hint = "the server refused the message"
	case StageRcpt:
		hint = "the server refused the recipient"
	}
	return fmt.Sprintf("%s %s: %v", e.Protocol, hint, e.Err)
}

func (e *ConnError) Unwrap() error { return e.Err }

func (d *Dialer) timeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return 20 * time.Second
}

func (d *Dialer) tlsConfig(host string) *tls.Config {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if d.TLSConfig != nil {
		cfg = d.TLSConfig.Clone()
	}
	cfg.ServerName = host
	return cfg
}

// dial opens a TCP connection with a deadline covering the whole session.
func (d *Dialer) route(ep Endpoint) Endpoint {
	if d.Route != nil {
		return d.Route(ep)
	}
	return ep
}

func (d *Dialer) dial(ctx context.Context, ep Endpoint, protocol string) (net.Conn, error) {
	return d.dialFor(ctx, ep, protocol, d.timeout())
}

func (d *Dialer) dialFor(ctx context.Context, ep Endpoint, protocol string, session time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	nd := net.Dialer{Control: d.Control}
	conn, err := nd.DialContext(ctx, "tcp", net.JoinHostPort(ep.Host, strconv.Itoa(ep.Port)))
	if err != nil {
		return nil, &ConnError{protocol, StageConnect, err}
	}
	conn.SetDeadline(time.Now().Add(session)) //nolint:errcheck
	if ep.ImplicitTLS {
		tc := tls.Client(conn, d.tlsConfig(ep.Host))
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, &ConnError{protocol, StageTLS, err}
		}
		return tc, nil
	}
	return conn, nil
}

func (d *Dialer) smtpClient(ctx context.Context, ep Endpoint, cred Credentials) (*smtp.Client, error) {
	ep = d.route(ep)
	conn, err := d.dial(ctx, ep, "SMTP")
	if err != nil {
		return nil, err
	}
	var c *smtp.Client
	if ep.ImplicitTLS {
		c = smtp.NewClient(conn)
	} else if c, err = smtp.NewClientStartTLS(conn, d.tlsConfig(ep.Host)); err != nil {
		conn.Close()
		return nil, &ConnError{"SMTP", StageTLS, err}
	}
	if err := c.Hello("localhost"); err != nil {
		c.Close()
		return nil, &ConnError{"SMTP", tlsOr(err, StageConnect), err}
	}
	var auth sasl.Client
	switch {
	case cred.Token != "":
		auth = xoauth2{cred.Username, cred.Token}
	case c.SupportsAuth(sasl.Plain):
		auth = sasl.NewPlainClient("", cred.Username, cred.Password)
	case c.SupportsAuth(sasl.Login):
		auth = sasl.NewLoginClient(cred.Username, cred.Password)
	default:
		c.Close()
		return nil, &ConnError{"SMTP", StageAuth, errors.New("server offers no password sign-in method")}
	}
	if err := c.Auth(auth); err != nil {
		c.Close()
		return nil, &ConnError{"SMTP", tlsOr(err, StageAuth), err}
	}
	return c, nil
}

// CheckSMTP signs in to the SMTP server and disconnects without sending.
func (d *Dialer) CheckSMTP(ctx context.Context, ep Endpoint, cred Credentials) error {
	if Simulated(ep) {
		return nil
	}
	c, err := d.smtpClient(ctx, ep, cred)
	if err != nil {
		return err
	}
	c.Quit() //nolint:errcheck
	return nil
}

// Send delivers one message (full RFC 5322 bytes) and returns the server's reply.
func (d *Dialer) Send(ctx context.Context, ep Endpoint, cred Credentials, from string, to []string, msg io.Reader) (string, error) {
	if Simulated(ep) {
		// A test workspace's send: recorded as sent like any other, and
		// delivered nowhere.
		_, err := io.Copy(io.Discard, msg)
		return "2.0.0 simulated: not delivered", err
	}
	c, err := d.smtpClient(ctx, ep, cred)
	if err != nil {
		return "", err
	}
	defer c.Close()
	if err := c.Mail(from, nil); err != nil {
		return "", &ConnError{"SMTP", StageSend, err}
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt, nil); err != nil {
			return "", &ConnError{"SMTP", StageRcpt, err}
		}
	}
	w, err := c.Data()
	if err != nil {
		return "", &ConnError{"SMTP", StageSend, err}
	}
	if _, err := io.Copy(w, msg); err != nil {
		return "", &ConnError{"SMTP", StageSend, err}
	}
	resp, err := w.CloseWithResponse()
	if err != nil {
		return "", &ConnError{"SMTP", StageSend, err}
	}
	c.Quit() //nolint:errcheck
	return resp.StatusText, nil
}

// CheckIMAP signs in, opens INBOX and logs out.
func (d *Dialer) CheckIMAP(ctx context.Context, ep Endpoint, cred Credentials) error {
	if Simulated(ep) {
		return nil
	}
	c, err := d.IMAP(ctx, ep, cred)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		return &ConnError{"IMAP", StageMailbox, err}
	}
	c.Logout().Wait() //nolint:errcheck
	return nil
}

// IMAP returns a signed-in IMAP client for a short session. The caller
// closes it.
func (d *Dialer) IMAP(ctx context.Context, ep Endpoint, cred Credentials) (*imapclient.Client, error) {
	return d.IMAPSession(ctx, ep, cred, d.timeout())
}

// IMAPSession is IMAP with a session that may last up to session, for
// longer work such as syncing an inbox.
func (d *Dialer) IMAPSession(ctx context.Context, ep Endpoint, cred Credentials, session time.Duration) (*imapclient.Client, error) {
	if Simulated(ep) {
		return nil, ErrSimulated
	}
	ep = d.route(ep)
	conn, err := d.dialFor(ctx, ep, "IMAP", session)
	if err != nil {
		return nil, err
	}
	opts := &imapclient.Options{TLSConfig: d.tlsConfig(ep.Host)}
	var c *imapclient.Client
	if ep.ImplicitTLS {
		c = imapclient.New(conn, opts)
	} else if c, err = imapclient.NewStartTLS(conn, opts); err != nil {
		conn.Close()
		return nil, &ConnError{"IMAP", StageTLS, err}
	}
	if cred.Token != "" {
		err = c.Authenticate(xoauth2{cred.Username, cred.Token})
	} else {
		err = c.Login(cred.Username, cred.Password).Wait()
	}
	if err != nil {
		c.Close()
		return nil, &ConnError{"IMAP", tlsOr(err, StageAuth), err}
	}
	return c, nil
}

// tlsOr reports StageTLS for certificate and handshake failures, which some
// clients only surface on the first command after STARTTLS.
func tlsOr(err error, stage Stage) Stage {
	var cv *tls.CertificateVerificationError
	var ua x509.UnknownAuthorityError
	var hn x509.HostnameError
	var rec tls.RecordHeaderError
	var alert tls.AlertError
	if errors.As(err, &cv) || errors.As(err, &ua) || errors.As(err, &hn) || errors.As(err, &rec) || errors.As(err, &alert) {
		return StageTLS
	}
	return stage
}

// IsHardBounce reports whether err is a permanent refusal of the recipient
// (a 5xx reply to RCPT TO), as opposed to a problem with the sender or a
// temporary failure.
func IsHardBounce(err error) bool {
	var ce *ConnError
	var se *smtp.SMTPError
	return errors.As(err, &ce) && ce.Stage == StageRcpt && errors.As(err, &se) && se.Code >= 500 && se.Code < 600
}

// IsTemporary reports whether err is a 4xx reply, worth retrying later.
func IsTemporary(err error) bool {
	var se *smtp.SMTPError
	return errors.As(err, &se) && se.Code >= 400 && se.Code < 500
}
