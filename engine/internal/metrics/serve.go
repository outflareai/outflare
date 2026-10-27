package metrics

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Options for the private metrics listener.
type Options struct {
	// Token, when set, must arrive as "Authorization: Bearer <token>".
	Token string
	// Pprof also serves Go's profiler at /debug/pprof/, to find what is
	// using CPU or memory. Profiles can hold request data, so only turn it
	// on where the listener is private.
	Pprof bool
}

// Handler serves /metrics (and /debug/pprof/ with Pprof) on its own mux,
// apart from the API, so nothing here is ever reachable on the public port.
func Handler(o Options) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(Registry, promhttp.HandlerOpts{
		Timeout:             20 * time.Second,
		MaxRequestsInFlight: 4,
	}))
	if o.Pprof {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
	if o.Token == "" {
		return mux
	}
	want := []byte("Bearer " + o.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="outflare-metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// Serve listens on addr until ctx ends. It returns an error only if the
// listener could not start or failed.
func Serve(ctx context.Context, addr string, o Options) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: Handler(o), ReadHeaderTimeout: 10 * time.Second, WriteTimeout: 2 * time.Minute}
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx) //nolint:errcheck
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-done
	return nil
}
