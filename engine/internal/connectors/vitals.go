package connectors

import "context"

const CapWebVitals = "web_vitals.read"

type WebVital struct {
	Page      string
	Device    string
	Day       string
	Metric    string
	P75       float64
	GoodShare float64
}
type WebVitalsReader interface {
	Connector
	WebVitals(context.Context, *Client, Creds, string, string) ([]WebVital, error)
}
