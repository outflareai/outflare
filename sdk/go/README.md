# Outflare for Go

Send what people do in your product to [Outflare](https://outflare.ai) from a Go server. It uses only the standard library.

```sh
go get github.com/outflareai/outflare/sdk/go
```

```go
import outflare "github.com/outflareai/outflare/sdk/go"

tracker, err := outflare.New(outflare.Config{Token: os.Getenv("OUTFLARE_TOKEN"), Brand: "northpeak"})
if err != nil {
	log.Fatal(err)
}
defer tracker.Close() // sends what waits before your program exits

tracker.Identify(outflare.Identify{UserID: "u_123", Traits: map[string]any{"email": "ana@acme.example", "plan": "pro"}})
tracker.Track(outflare.Track{UserID: "u_123", Event: "Order Completed", Properties: map[string]any{"total": 42, "currency": "USD"}})
tracker.Group(outflare.Group{UserID: "u_123", GroupID: "org_7", Traits: map[string]any{"name": "Acme"}})
```

The token is an API token with `customers.manage`, so keep it on your servers. `Brand` is the brand's id or slug; `BaseURL` points at a self-hosted server.

Events are queued in memory and sent in batches (20 at a time, or every 10 seconds) to `POST /v1/brands/{brand}/events`. A batch the server can't take is tried again three times with a growing wait; a refusal such as a bad token is not. Every call returns the event's message id, which the server uses to store a retried event once. There are also `Page`, `Screen` and `Alias`. `Flush(ctx)` sends now, and `Config.OnError` hears about batches that were given up on. A `Tracker` is safe to share between goroutines.

## Develop

```sh
go vet ./... && go test -race ./...
```

## License

Apache-2.0.
