package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// Chrome opens headless Chrome (or Chromium) on this host, one fresh
// profile per task: no cookies, storage or history from any other task.
// The task's boundary is enforced by intercepting every request the
// browser makes and failing the ones outside it, so a page can't fetch,
// redirect or post anywhere the task wasn't given.
type Chrome struct {
	// Path is the browser's executable; empty finds one (FindChrome).
	Path string
}

// FindChrome returns the first Chrome or Chromium on PATH, or "".
func FindChrome() string {
	if p := os.Getenv("OUTFLARE_BROWSER"); p != "" && p != "auto" && p != "off" {
		return p
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome", "chromium-headless-shell"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

type chromeDriver struct {
	ctx    context.Context
	cancel context.CancelFunc
	b      Boundary
	mu     sync.Mutex
	refs   int
}

// Open starts a browser for one task.
func (c Chrome) Open(ctx context.Context, b Boundary) (Driver, error) {
	path := c.Path
	if path == "" {
		path = FindChrome()
	}
	if path == "" {
		return nil, errors.New("no Chrome or Chromium on this host (set OUTFLARE_BROWSER to its path)")
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(path),
		chromedp.NoSandbox,
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.WindowSize(1280, 900),
		chromedp.UserAgent("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 OutflareAgent"),
	)
	actx, acancel := chromedp.NewExecAllocator(context.WithoutCancel(ctx), opts...)
	bctx, bcancel := chromedp.NewContext(actx)
	d := &chromeDriver{ctx: bctx, cancel: func() { bcancel(); acancel() }, b: b}
	// Every request is paused and checked before it leaves; one outside
	// the boundary fails as if blocked by the client. The subscription is
	// made before interception is turned on, so no request slips past.
	paused := chromedp.Events(bctx, fetch.RequestPaused)
	if _, err := chromedp.Run(bctx, chromedp.Evaluate[chromedp.Void]("1")); err != nil {
		d.cancel()
		return nil, fmt.Errorf("starting the browser: %w", err)
	}
	target := chromedp.FromContext(bctx).Target
	go func() {
		for ev, err := range paused {
			if err != nil {
				return
			}
			if err := b.Check(bctx, ev.Request.URL); err != nil {
				_, _ = cdp.Call(bctx, target, fetch.FailRequest, fetch.FailRequestParams{RequestID: ev.RequestID, ErrorReason: network.ErrorReasonBlockedByClient})
				continue
			}
			_, _ = cdp.Call(bctx, target, fetch.ContinueRequest, fetch.ContinueRequestParams{RequestID: ev.RequestID})
		}
	}()
	if _, err := cdp.Call(bctx, target, fetch.Enable, fetch.EnableParams{Patterns: []*fetch.RequestPattern{{URLPattern: "*", RequestStage: fetch.RequestStageRequest}}}); err != nil {
		d.cancel()
		return nil, fmt.Errorf("intercepting requests: %w", err)
	}
	return d, nil
}

func (d *chromeDriver) Navigate(ctx context.Context, url string) error {
	if err := d.b.Check(ctx, url); err != nil {
		return err
	}
	return d.do(ctx, chromedp.Navigate(url), chromedp.Sleep(300*time.Millisecond))
}

// snapshotJS numbers the page's interactive elements (data-outflare-ref)
// and returns the visible text and the elements as JSON.
const snapshotJS = `(() => {
  const roles = (el) => {
    const t = el.tagName.toLowerCase(), ty = (el.getAttribute('type') || '').toLowerCase();
    if (t === 'a') return 'link';
    if (t === 'button' || (t === 'input' && (ty === 'submit' || ty === 'button'))) return 'button';
    if (t === 'input' && ty === 'checkbox') return 'checkbox';
    if (t === 'input' && ty === 'radio') return 'radio';
    if (t === 'input' || t === 'textarea') return ty === 'password' ? 'password' : 'textbox';
    if (t === 'select') return 'select';
    const r = el.getAttribute('role'); if (r) return r;
    return 'element';
  };
  const label = (el) => {
    const a = el.getAttribute('aria-label'); if (a) return a;
    if (el.id) { const f = document.querySelector('label[for="' + CSS.escape(el.id) + '"]'); if (f && f.innerText.trim()) return f.innerText.trim(); }
    const p = el.closest('label'); if (p && p.innerText.trim()) return p.innerText.trim();
    const l = el.getAttribute('placeholder') || el.getAttribute('name') || el.getAttribute('title') || '';
    if (l) return l;
    return (el.innerText || el.value || el.alt || '').trim().slice(0, 120);
  };
  const visible = (el) => { const r = el.getBoundingClientRect(); const s = getComputedStyle(el); return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none'; };
  let n = 0; const els = [];
  document.querySelectorAll('a[href], button, input:not([type=hidden]), textarea, select, [role=button], [role=link], [role=textbox], [role=menuitem], [role=tab], [contenteditable=true]').forEach((el) => {
    if (!visible(el)) return;
    n++; el.setAttribute('data-outflare-ref', String(n));
    const role = roles(el);
    const value = role === 'password' ? '' : ((el.value || '') + '').slice(0, 200);
    els.push({ref: n, role, label: label(el), value});
  });
  return JSON.stringify({url: location.href, title: document.title, text: (document.body ? document.body.innerText : '').slice(0, 60000), elements: els});
})()`

func (d *chromeDriver) Page(ctx context.Context) (Page, error) {
	raw, err := withDeadline(ctx, d, func(tctx context.Context) (string, error) {
		return chromedp.Run(tctx, chromedp.Evaluate[string](snapshotJS))
	})
	if err != nil {
		return Page{}, err
	}
	var out struct {
		URL      string    `json:"url"`
		Title    string    `json:"title"`
		Text     string    `json:"text"`
		Elements []Element `json:"elements"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return Page{}, err
	}
	d.mu.Lock()
	d.refs = len(out.Elements)
	d.mu.Unlock()
	return Page{URL: out.URL, Title: out.Title, Text: strings.TrimSpace(out.Text), Elements: out.Elements}, nil
}

func (d *chromeDriver) sel(ref int) (string, error) {
	d.mu.Lock()
	n := d.refs
	d.mu.Unlock()
	if ref < 1 || ref > n {
		return "", fmt.Errorf("no element [%d] on the page as last read; read the page again", ref)
	}
	return fmt.Sprintf(`[data-outflare-ref="%d"]`, ref), nil
}

func (d *chromeDriver) Click(ctx context.Context, ref int) error {
	s, err := d.sel(ref)
	if err != nil {
		return err
	}
	return d.do(ctx, chromedp.Click(chromedp.CSS(s)), chromedp.Sleep(500*time.Millisecond))
}

func (d *chromeDriver) Type(ctx context.Context, ref int, text string, submit bool) error {
	s, err := d.sel(ref)
	if err != nil {
		return err
	}
	sel := chromedp.CSS(s)
	// Clearing works on inputs and textareas; an editable element keeps
	// what it had and gets the text appended.
	_ = d.do(ctx, chromedp.Clear(sel))
	acts := []chromedp.Action[chromedp.Void]{chromedp.Focus(sel), chromedp.SendKeys(sel, text)}
	if submit {
		acts = append(acts, chromedp.SendKeys(sel, "\r"), chromedp.Sleep(500*time.Millisecond))
	}
	return d.do(ctx, acts...)
}

// do runs actions with the task's own deadline and the driver's: a page
// that never settles can't hold a worker.
func (d *chromeDriver) do(ctx context.Context, acts ...chromedp.Action[chromedp.Void]) error {
	_, err := withDeadline(ctx, d, func(tctx context.Context) (chromedp.Void, error) {
		return chromedp.Void{}, chromedp.Do(tctx, acts...)
	})
	return err
}

func withDeadline[T any](ctx context.Context, d *chromeDriver, f func(context.Context) (T, error)) (T, error) {
	tctx, cancel := context.WithTimeout(d.ctx, 30*time.Second)
	defer cancel()
	type res struct {
		v   T
		err error
	}
	done := make(chan res, 1)
	go func() {
		v, err := f(tctx)
		done <- res{v, err}
	}()
	select {
	case r := <-done:
		return r.v, r.err
	case <-ctx.Done():
		cancel()
		var zero T
		return zero, ctx.Err()
	}
}

func (d *chromeDriver) Close() error {
	d.cancel()
	return nil
}
