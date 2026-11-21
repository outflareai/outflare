package track

import (
	"context"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Events for what the server only wrote to its log. Most errors have an
// event of their own (api.called, job.failed, a track.Fail). The rest, a
// background loop that logs and carries on, a clean-up that failed, would
// be seen only by someone reading the log. LogEvents turns each of those
// lines into an event too, so every error is counted in one place.
const (
	ErrorLogged   = "server.error_logged"   // a line at error level
	WarningLogged = "server.warning_logged" // a line at warning level
)

// logEvery is how often the same line becomes an event. A loop that logs
// the same error thousands of times a minute sends one event a minute,
// which carries how many it stands for in count.
const logEvery = time.Minute

// alreadyEvents are log lines whose failure is recorded as an event by the
// code that logs them, so a second event would count it twice.
var alreadyEvents = map[string]bool{
	"internal error":        true, // api.called
	"panic in handler":      true, // server.panicked
	"job dead":              true, // job.dead
	"job failed, retrying":  true, // job.failed
	"http":                  true, // api.called
	"event":                 true,
	"product events: send":  true, // the event pipeline itself: an event about
	"product events: store": true, // a failure to record events would loop
}

type logEvents struct {
	slog.Handler
	seen *logSeen
}

type logSeen struct {
	mu   sync.Mutex
	last map[string]time.Time
	held map[string]int // lines not sent since the last event
}

// LogEvents wraps a log handler so every line at warning level or above
// that no event covers already is also recorded as an event:
// server.error_logged or server.warning_logged, with where (the function
// that logged it), message (the line's own text when it is plain words,
// never its attributes, which can hold customer data) and count.
func LogEvents(h slog.Handler) slog.Handler {
	return &logEvents{Handler: h, seen: &logSeen{last: map[string]time.Time{}, held: map[string]int{}}}
}

func (l *logEvents) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &logEvents{Handler: l.Handler.WithAttrs(attrs), seen: l.seen}
}

func (l *logEvents) WithGroup(name string) slog.Handler {
	return &logEvents{Handler: l.Handler.WithGroup(name), seen: l.seen}
}

func (l *logEvents) Handle(ctx context.Context, r slog.Record) error {
	err := l.Handler.Handle(ctx, r)
	if r.Level >= slog.LevelWarn {
		l.record(ctx, r)
	}
	return err
}

func (l *logEvents) record(ctx context.Context, r slog.Record) {
	if alreadyEvents[r.Message] || strings.HasPrefix(r.Message, "product event") {
		return
	}
	e := Event{Name: WarningLogged, Outcome: Failure, ErrorCode: "logged_warning", Surface: "system", ActorType: "system", At: r.Time.UTC()}
	if r.Level >= slog.LevelError {
		e.Name, e.Outcome, e.ErrorCode = ErrorLogged, Error, "logged_error"
	}
	covered := false
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "error_code":
			// track.Fail and the request log write the code of an event
			// they record themselves.
			covered = true
			return false
		case "request_id":
			e.RequestID = a.Value.String()
		case "workspace_id":
			e.WorkspaceID = a.Value.String()
		}
		return true
	})
	if covered {
		return
	}
	where := logCaller(r.PC)
	msg := plainWords(r.Message)
	key := e.Name + "|" + where + "|" + msg
	now := time.Now()
	l.seen.mu.Lock()
	if last, ok := l.seen.last[key]; ok && now.Sub(last) < logEvery {
		l.seen.held[key]++
		l.seen.mu.Unlock()
		return
	}
	if len(l.seen.last) > 5000 {
		clear(l.seen.last)
		clear(l.seen.held)
	}
	count := 1 + l.seen.held[key]
	delete(l.seen.held, key)
	l.seen.last[key] = now
	l.seen.mu.Unlock()
	e.Props = map[string]any{"where": where, "count": count}
	if msg != "" {
		e.Props["message"] = msg
	}
	Emit(ctx, e)
}

// logCaller names the function that wrote a log line, such as
// "jobs.(*Queue).sweep": a place in the code, the same from one release to
// the next, where a line number would move.
func logCaller(pc uintptr) string {
	if pc == 0 {
		return "unknown"
	}
	f, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	name := f.Function
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	// A closure is named after the function it is written in.
	if i := strings.Index(name, ".func"); i > 0 {
		name = name[:i]
	}
	if name == "" {
		return "unknown"
	}
	return name
}

// plainWords is a log line's text when it is what the code says: short,
// and with nothing in it that looks like an address, a link or a value
// formatted into it. Anything else is left out; where still names the line.
func plainWords(m string) string {
	if len(m) > 100 || strings.ContainsAny(m, "@\n\"'=") || strings.Contains(m, "://") {
		return ""
	}
	return m
}
