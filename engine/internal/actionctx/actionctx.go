// Package actionctx carries the action (engine ADR 13) a request runs as,
// so spend booked while it runs is linked to it and counts toward the
// grant's spend limits.
package actionctx

import "context"

type ctxKey struct{}

// WithID returns ctx carrying the action a request runs as.
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// ID returns the action the request runs as, or "" when there is none.
func ID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}
