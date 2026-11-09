package outcomes

import "context"

type brandKey struct{}

// WithBrand returns ctx carrying the brand that charges recorded while it
// runs belong to: a brand route's brand, or the brand a job works for.
// Record gives a charge with no sequence and no brand of its own this one,
// so a brand's searches, rank checks and SEO reports are its own costs
// and never shared with other brands.
func WithBrand(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, brandKey{}, id)
}

// BrandFrom returns the brand WithBrand put on ctx, or "".
func BrandFrom(ctx context.Context) string {
	id, _ := ctx.Value(brandKey{}).(string)
	return id
}
