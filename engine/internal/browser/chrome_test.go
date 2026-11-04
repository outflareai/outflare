package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Real Chrome against a local site: pages read as numbered elements, a
// form filled and submitted, and a request outside the boundary (an image
// from another host, a navigation there) refused at the network.
func TestChromeDriverOnALocalSite(t *testing.T) {
	if FindChrome() == "" {
		t.Skip("no Chrome on this host")
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the other host was reached: %s", r.URL)
	}))
	defer other.Close()
	// The other host is the same loopback address by another name, so the
	// boundary (hosts, not addresses) tells them apart.
	otherURL := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<html><head><title>Sign in</title></head><body><h1>Welcome back</h1>
				<img src="` + otherURL + `/pixel.gif" alt="">
				<form method="post" action="/login"><label for="e">Email</label><input id="e" name="email">
				<input type="password" name="pw" placeholder="Password"><button type="submit">Sign in</button></form>
				<a href="` + otherURL + `/away">Leave</a></body></html>`))
		case "/login":
			_ = r.ParseForm()
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<html><head><title>Home</title></head><body><p>Signed in as ` + r.FormValue("email") + `.</p><p>pw=` + strings.Repeat("*", len(r.FormValue("pw"))) + `</p></body></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()
	host := NormalizeHost(site.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	d, err := Chrome{}.Open(ctx, Boundary{Hosts: []string{host}, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Navigate(ctx, site.URL+"/"); err != nil {
		t.Fatal(err)
	}
	p, err := d.Page(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Sign in" || !strings.Contains(p.Text, "Welcome back") || len(p.Elements) != 4 {
		t.Fatalf("page: %+v", p)
	}
	var email, pw, btn int
	for _, e := range p.Elements {
		switch {
		case e.Role == "textbox" && e.Label == "Email":
			email = e.Ref
		case e.Role == "password":
			pw = e.Ref
		case e.Role == "button":
			btn = e.Ref
		}
	}
	if email == 0 || pw == 0 || btn == 0 {
		t.Fatalf("elements: %+v", p.Elements)
	}
	if err := d.Type(ctx, email, "ops@acme.example", false); err != nil {
		t.Fatal(err)
	}
	if err := d.Type(ctx, pw, "hunter22", false); err != nil {
		t.Fatal(err)
	}
	if err := d.Click(ctx, btn); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	p, err = d.Page(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Home" || !strings.Contains(p.Text, "Signed in as ops@acme.example") || !strings.Contains(p.Text, "pw=********") {
		t.Fatalf("after submit: %+v", p)
	}
	if err := d.Navigate(ctx, otherURL+"/away"); err == nil {
		t.Fatal("navigating outside the boundary must fail")
	}
	if err := d.Click(ctx, 99); err == nil {
		t.Fatal("a ref the page doesn't have must fail")
	}
}

// A session's cookies read from one browser and set in a new one sign
// the second in, the way a saved session does.
func TestChromeDriverKeepsCookies(t *testing.T) {
	if FindChrome() == "" {
		t.Skip("no Chrome on this host")
	}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "s3ss10n", Path: "/", HttpOnly: true, Expires: time.Now().Add(time.Hour)})
		}
		who := "nobody"
		if c, err := r.Cookie("sid"); err == nil && c.Value == "s3ss10n" {
			who = "ops"
		}
		w.Write([]byte(`<html><head><title>T</title></head><body><p>Signed in as ` + who + `.</p></body></html>`))
	}))
	defer site.Close()
	b := Boundary{Hosts: []string{NormalizeHost(site.URL)}, AllowPrivate: true}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	first, err := Chrome{}.Open(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Navigate(ctx, site.URL+"/login"); err != nil {
		t.Fatal(err)
	}
	cookies, err := first.(SessionKeeper).Cookies(ctx)
	first.Close()
	if err != nil || len(Keep(cookies, b.Hosts, time.Now())) != 1 {
		t.Fatalf("cookies: %v %v", cookies, err)
	}
	second, err := Chrome{}.Open(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := second.(SessionKeeper).SetCookies(ctx, Keep(cookies, b.Hosts, time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := second.Navigate(ctx, site.URL+"/"); err != nil {
		t.Fatal(err)
	}
	p, err := second.Page(ctx)
	if err != nil || !strings.Contains(p.Text, "Signed in as ops.") {
		t.Fatalf("restored: %+v %v", p, err)
	}
}
