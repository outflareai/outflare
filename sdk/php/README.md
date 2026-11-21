# Outflare for PHP

Send what people do in your product to [Outflare](https://outflare.ai) from a PHP server. It needs PHP 8.1 with the curl extension and nothing else.

```sh
composer require outflare/outflare
```

```php
$tracker = new \Outflare\Tracker(['brand' => 'northpeak']); // reads OUTFLARE_TOKEN and OUTFLARE_URL

$tracker->identify('u_123', ['email' => 'ana@acme.example', 'plan' => 'pro']);
$tracker->track('u_123', 'Order Completed', ['total' => 42, 'currency' => 'USD']);
$tracker->group('u_123', 'org_7', ['name' => 'Acme']);
```

The token is an API token with `customers.manage`, so keep it on your servers.

Events are queued in memory and sent in batches to `POST /v1/brands/{brand}/events`: once 20 wait, when you call `flush()`, and when the request ends. A batch the server can't take is tried again three times with a growing wait; a refusal such as a bad token is not. Every call returns the event's message id, which the server uses to store a retried event once. There are also `page`, `screen` and `alias`, and every call takes an options array with `anonymous_id`, `timestamp` and `message_id`. `on_error` takes a callable that hears about batches that were given up on.

Sending happens in the request that made the events. To keep it off the response time, call `fastcgi_finish_request()` before the request ends, or send from a queue worker.

## Develop

```sh
php tests/run.php
```

## License

Apache-2.0.
