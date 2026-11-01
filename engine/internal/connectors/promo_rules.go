package connectors

import (
	"context"
	"time"
)

// PromoCodeRules describe the customer's checkout discount, independently of
// the partner's commission. Existing coupons can supply the discount instead.
type PromoCodeRules struct {
	Disabled       bool       `json:"disabled"`
	StartsAt       *time.Time `json:"starts_at"`
	EndsAt         *time.Time `json:"ends_at"`
	PercentBP      int        `json:"percent_bp"`
	AmountCents    int64      `json:"amount_cents"`
	Currency       string     `json:"currency"`
	MaxRedemptions int        `json:"max_redemptions"`
	FirstOrderOnly bool       `json:"first_order_only"`
	MinOrderCents  int64      `json:"min_order_cents"`
	Duration       string     `json:"duration"`
	Months         int        `json:"months"`
	ProductIDs     []string   `json:"product_ids"`
}

// ManagedPromoCodeWriter applies native checkout restrictions with the
// workspace's own billing connection. Updating an existing code only toggles it.
type ManagedPromoCodeWriter interface {
	CreateManagedPromoCode(context.Context, *Client, Creds, string, string, PromoCodeRules, map[string]string) (string, error)
	SetPromoCodeActive(context.Context, *Client, Creds, string, bool) error
}
