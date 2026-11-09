package tgsession

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"golang.org/x/net/proxy"

	"github.com/outflareai/outflare/engine/internal/connectors/tgapp"
)

// MTProto signs in and reads through Telegram's own protocol, as a linked
// device named Outflare, so the account's owner sees it in Telegram's
// device list (Settings > Devices) and can end it there.
type MTProto struct {
	// Dial makes direct connections (nil: net.Dialer). A proxy on the
	// app dials through it.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

var _ Telegram = MTProto{}

func (m MTProto) client(app tgapp.App, stored []byte, handler telegram.UpdateHandler) (*telegram.Client, *session.StorageMemory, error) {
	mem := &session.StorageMemory{}
	if len(stored) > 0 {
		if err := mem.StoreSession(context.Background(), stored); err != nil {
			return nil, nil, err
		}
	}
	dial := m.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 20 * time.Second}).DialContext
	}
	if app.Proxy != "" {
		u, err := url.Parse(app.Proxy)
		if err != nil {
			return nil, nil, err
		}
		if u.Scheme == "socks5h" {
			u.Scheme = "socks5"
		}
		d, err := proxy.FromURL(u, dialerFunc(dial))
		if err != nil {
			return nil, nil, err
		}
		cd, ok := d.(proxy.ContextDialer)
		if !ok {
			return nil, nil, fmt.Errorf("proxy %s can't dial with a context", u.Scheme)
		}
		dial = cd.DialContext
	}
	opts := telegram.Options{
		SessionStorage: mem,
		Resolver:       dcs.Plain(dcs.PlainOptions{Dial: dial}),
		Device: telegram.DeviceConfig{
			DeviceModel: "Outflare", SystemVersion: "Server", AppVersion: "1.0",
			SystemLangCode: "en", LangCode: "en",
		},
		NoUpdates: handler == nil,
	}
	if handler != nil {
		opts.UpdateHandler = handler
	}
	return telegram.NewClient(app.ID, app.Hash, opts), mem, nil
}

type dialerFunc func(ctx context.Context, network, addr string) (net.Conn, error)

func (f dialerFunc) Dial(network, addr string) (net.Conn, error) {
	return f(context.Background(), network, addr)
}

func (f dialerFunc) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return f(ctx, network, addr)
}

func saved(mem *session.StorageMemory) []byte {
	b, _ := mem.Bytes(nil)
	return b
}

// QRLogin runs Telegram's QR sign-in (core.telegram.org/api/qr-login).
func (m MTProto) QRLogin(ctx context.Context, app tgapp.App, stored []byte, show func(string, time.Time) error) (Result, error) {
	d := tg.NewUpdateDispatcher()
	loggedIn := qrlogin.OnLoginToken(d)
	cl, mem, err := m.client(app, stored, d)
	if err != nil {
		return Result{}, err
	}
	var res Result
	err = cl.Run(ctx, func(ctx context.Context) error {
		if self, err := cl.Self(ctx); err == nil {
			res.Self = selfOf(self)
			return nil
		}
		_, err := cl.QR().Auth(ctx, loggedIn, func(ctx context.Context, t qrlogin.Token) error {
			return show(t.URL(), t.Expires())
		})
		if needsPassword(err) {
			res.NeedPassword = true
			if p, perr := cl.API().AccountGetPassword(ctx); perr == nil {
				res.PasswordHint = p.Hint
			}
			return nil
		}
		if err != nil {
			return classify(err)
		}
		self, err := cl.Self(ctx)
		if err != nil {
			return classify(err)
		}
		res.Self = selfOf(self)
		return nil
	})
	res.Session = saved(mem)
	return res, err
}

// Password finishes a sign-in with the account's two-step password.
func (m MTProto) Password(ctx context.Context, app tgapp.App, stored []byte, password string) (Result, error) {
	cl, mem, err := m.client(app, stored, nil)
	if err != nil {
		return Result{}, err
	}
	var res Result
	err = cl.Run(ctx, func(ctx context.Context) error {
		if _, err := cl.Auth().Password(ctx, password); err != nil {
			if errors.Is(err, auth.ErrPasswordInvalid) {
				return ErrWrongPassword
			}
			return classify(err)
		}
		self, err := cl.Self(ctx)
		if err != nil {
			return classify(err)
		}
		res.Self = selfOf(self)
		return nil
	})
	res.Session = saved(mem)
	return res, err
}

// Check reads the account and its protection. Read only.
func (m MTProto) Check(ctx context.Context, app tgapp.App, stored []byte) (Health, error) {
	cl, mem, err := m.client(app, stored, nil)
	if err != nil {
		return Health{}, err
	}
	var h Health
	err = cl.Run(ctx, func(ctx context.Context) error {
		self, err := cl.Self(ctx)
		if err != nil {
			return classify(err)
		}
		h.Self = *selfOf(self)
		p, err := cl.API().AccountGetPassword(ctx)
		if err != nil {
			return classify(err)
		}
		h.TwoStep = p.HasPassword
		_, h.LoginEmail = p.GetLoginEmailPattern()
		auths, err := cl.API().AccountGetAuthorizations(ctx)
		if err != nil {
			return classify(err)
		}
		for _, a := range auths.Authorizations {
			if !a.Current {
				h.OtherSessions++
			}
		}
		return nil
	})
	h.Session = saved(mem)
	return h, err
}

// LogOut ends the session at Telegram.
func (m MTProto) LogOut(ctx context.Context, app tgapp.App, stored []byte) error {
	cl, _, err := m.client(app, stored, nil)
	if err != nil {
		return err
	}
	return cl.Run(ctx, func(ctx context.Context) error {
		if _, err := cl.API().AuthLogOut(ctx); err != nil && !auth.IsUnauthorized(err) {
			return classify(err)
		}
		return nil
	})
}

func selfOf(u *tg.User) *Self {
	s := &Self{ID: u.ID, Username: strings.ToLower(u.Username), Name: strings.TrimSpace(u.FirstName + " " + u.LastName)}
	if u.Phone != "" {
		s.Phone = "+" + strings.TrimPrefix(u.Phone, "+")
	}
	return s
}

func needsPassword(err error) bool {
	return err != nil && (errors.Is(err, auth.ErrPasswordAuthNeeded) || tgerr.Is(err, "SESSION_PASSWORD_NEEDED"))
}

func classify(err error) error {
	switch {
	case err == nil:
		return nil
	case auth.IsUnauthorized(err) || tgerr.Is(err, "AUTH_KEY_UNREGISTERED", "SESSION_REVOKED", "SESSION_EXPIRED", "USER_DEACTIVATED", "USER_DEACTIVATED_BAN"):
		return fmt.Errorf("%w: %v", ErrSignedOut, err)
	case tgerr.Is(err, "API_ID_INVALID", "API_ID_PUBLISHED_FLOOD"):
		return fmt.Errorf("%w: %v", ErrBadApp, err)
	}
	return err
}
