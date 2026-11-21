# Outflare for Ruby

Send what people do in your product to [Outflare](https://outflare.ai) from a Ruby server. It uses only the standard library.

```ruby
gem "outflare"
```

```ruby
require "outflare"

tracker = Outflare::Tracker.new(brand: "northpeak") # reads OUTFLARE_TOKEN and OUTFLARE_URL

tracker.identify(user_id: "u_123", traits: { email: "ana@acme.example", plan: "pro" })
tracker.track(user_id: "u_123", event: "Order Completed", properties: { total: 42, currency: "USD" })
tracker.group(user_id: "u_123", group_id: "org_7", traits: { name: "Acme" })

tracker.close # before the process exits; also runs at exit
```

The token is an API token with `customers.manage`, so keep it on your servers. In Rails, make one tracker in an initializer and share it.

Events are queued in memory and sent in batches (20 at a time, or every 10 seconds) to `POST /v1/brands/{brand}/events`. A batch the server can't take is tried again three times with a growing wait; a refusal such as a bad token is not. Every call returns the event's message id, which the server uses to store a retried event once. There are also `page`, `screen` and `alias`, and every call takes `anonymous_id:`, `timestamp:` and `message_id:`. `flush` sends now; `on_error:` takes a callable that hears about batches that were given up on. A background thread does the sending.

## Develop

```sh
ruby -Ilib -Itest test/tracker_test.rb
```

## License

Apache-2.0.
