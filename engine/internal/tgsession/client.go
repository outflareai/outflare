package tgsession

import (
	"context"
	"errors"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors/tgapp"
)

// Telegram is what the service needs from Telegram. MTProto talks to
// Telegram itself; tests use a fake.
type Telegram interface {
	// QRLogin starts or continues a QR sign-in with session (nil for a new
	// one). show is called with each fresh tg://login link until someone
	// scans it, the context ends, or Telegram answers.
	QRLogin(ctx context.Context, app tgapp.App, session []byte, show func(url string, expires time.Time) error) (Result, error)
	// Password finishes a sign-in that needs the two-step password.
	Password(ctx context.Context, app tgapp.App, session []byte, password string) (Result, error)
	// Check reads who the session is signed in as and how the account is
	// protected. It changes nothing.
	Check(ctx context.Context, app tgapp.App, session []byte) (Health, error)
	// LogOut ends the session at Telegram.
	LogOut(ctx context.Context, app tgapp.App, session []byte) error
	// Read opens the session and hands read to a Reader for as long as
	// it runs. It returns the session to keep afterwards.
	Read(ctx context.Context, app tgapp.App, session []byte, read func(Reader) error) ([]byte, error)
}

// Reader reads chats. Nothing it does changes the account: messages
// stay unread on the account's phone.
type Reader interface {
	// Dialogs calls each with the account's chats, most recent first,
	// until each returns false or there are no more.
	Dialogs(ctx context.Context, each func(Dialog) (bool, error)) error
	// History returns up to limit messages of a chat, newest first,
	// older than offsetID (0: the newest) and newer than minID.
	History(ctx context.Context, peer Peer, offsetID, minID, limit int) ([]Message, error)
}

// Peer is a chat as Telegram addresses it. AccessHash is the account's
// own key to the peer, needed to read it.
type Peer struct {
	Kind       string // user, group or channel
	ID         int64
	AccessHash int64
}

// Dialog is one of the account's chats.
type Dialog struct {
	Peer       Peer
	Title      string
	Username   string
	Phone      string
	TopMessage int
	Members    int
	// Bot, Self (Saved Messages) and Broadcast (a channel people only
	// read) are never synced.
	Bot, Self, Broadcast bool
}

// Message is one message in a chat.
type Message struct {
	ID       int
	Out      bool
	From     int64 // the sender's user id, 0 when unknown
	FromName string
	Text     string
	// Media is what is attached, if anything: photo, video, voice, file,
	// sticker, contact, location, poll or other. Files aren't copied.
	Media    string
	FileName string
	ReplyTo  int
	// Service messages are chat events (someone joined, a call).
	Service  bool
	At       time.Time
	EditedAt *time.Time
}

// WaitError is Telegram asking to slow down (FLOOD_WAIT).
type WaitError struct{ For time.Duration }

func (e *WaitError) Error() string { return "telegram asked to wait " + e.For.String() }

// Result is how a sign-in step ended. Session is always the latest one to
// keep, signed in or not.
type Result struct {
	Session      []byte
	NeedPassword bool
	PasswordHint string
	Self         *Self
}

// Self is who an account is.
type Self struct {
	ID       int64
	Username string
	Phone    string
	Name     string
}

// Health is what a check read.
type Health struct {
	Session       []byte
	Self          Self
	TwoStep       bool
	LoginEmail    bool
	OtherSessions int
}

// Errors Telegram answers that the service acts on.
var (
	// ErrSignedOut: the session no longer works (ended from a phone, or
	// the account was deleted).
	ErrSignedOut = errors.New("telegram session ended")
	// ErrWrongPassword: the two-step password was wrong.
	ErrWrongPassword = errors.New("wrong two-step password")
	// ErrBadApp: Telegram refused the app's api_id or api_hash.
	ErrBadApp = errors.New("telegram refused the app id or hash")
)
