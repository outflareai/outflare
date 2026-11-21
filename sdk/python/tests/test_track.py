import json

import httpx
import pytest

from outflare import Tracker, error_hash, looks_like_crawler


def tracker_with(answers=(), **kw):
    calls = []
    answers = list(answers)

    def handler(request):
        calls.append(request)
        status = answers.pop(0) if answers else 202
        if status == 0:
            raise httpx.ConnectError("network down")
        return httpx.Response(status, json={})

    kw.setdefault("flush_interval", 3600)
    return Tracker(
        "northpeak", "oft_test_not_a_real_token", "http://test/", transport=httpx.MockTransport(handler), **kw
    ), calls


def test_events_are_queued_batched_and_sent_with_the_token():
    t, calls = tracker_with()
    mid = t.track("u1", "Order Completed", {"total": 42})
    t.identify("u1", {"plan": "pro"})
    t.group("u1", "org_7")
    t.page(None, "Pricing", anonymous_id="a1")
    t.screen(None, "Home", anonymous_id="a1")
    t.alias("u1", "a1")
    assert calls == []
    t.flush()
    assert len(calls) == 1
    req = calls[0]
    assert str(req.url) == "http://test/v1/brands/northpeak/events"
    assert req.headers["authorization"] == "Bearer oft_test_not_a_real_token"
    body = json.loads(req.content)
    assert body["source"] == "python"
    assert [m["type"] for m in body["batch"]] == ["track", "identify", "group", "page", "screen", "alias"]
    assert body["batch"][0]["messageId"] == mid
    assert body["batch"][0]["event"] == "Order Completed"
    assert body["batch"][0]["context"]["library"]["name"] == "outflare-python"
    assert body["batch"][3]["anonymousId"] == "a1" and "userId" not in body["batch"][3]
    t.close()


def test_close_sends_what_waits_and_takes_no_more():
    t, calls = tracker_with()
    t.track("u1", "A")
    t.close()
    assert len(calls) == 1
    with pytest.raises(RuntimeError):
        t.track("u1", "B")


def test_a_busy_server_is_retried_and_a_refusal_is_not(monkeypatch):
    monkeypatch.setattr("outflare.track.time.sleep", lambda s: None)
    lost = []
    t, calls = tracker_with([503, 0, 202, 401], on_error=lambda e, n: lost.append((str(e), n)))
    t.track("u1", "A")
    t.flush()
    assert len(calls) == 3 and lost == []
    t.track("u1", "B")
    t.flush()
    assert len(calls) == 4
    assert lost == [("the server refused the batch (401)", 1)]
    t.close()


def test_calls_say_what_is_missing(monkeypatch):
    t, _ = tracker_with()
    with pytest.raises(ValueError):
        t.track(None, "A")
    with pytest.raises(ValueError):
        t.track("u1", "")
    with pytest.raises(TypeError):
        t.track("u1", "A", colour="red")
    t.close()
    monkeypatch.delenv("OUTFLARE_TOKEN", raising=False)
    with pytest.raises(ValueError):
        Tracker("northpeak")


def test_exposed_sends_experiment_viewed_only_for_a_version():
    t, calls = tracker_with()
    assert t.exposed(None, "pricing-headline", False, anonymous_id="a1") is None
    assert t.exposed(None, "pricing-headline", "faster", anonymous_id="a1")
    with pytest.raises(ValueError):
        t.exposed("u1", "", "faster")
    t.close()
    batch = json.loads(calls[0].content)["batch"]
    assert len(batch) == 1
    assert batch[0]["event"] == "Experiment Viewed"
    assert batch[0]["properties"] == {"flag": "pricing-headline", "variant": "faster", "source": "server"}


def test_capture_exception_sends_error_caught_without_addresses_or_keys():
    t, calls = tracker_with()

    def boom(n):
        try:
            raise TypeError(f"order {n} for ana@acme.example failed with sk_test_not_a_real_key_0000000000000000")
        except TypeError as err:
            return err

    t.capture_exception(boom(41), "u1", {"route": "/checkout", "type": "mine"})
    t.capture_exception(boom(42))
    t.capture_exception("plain text", anonymous_id="a1", handled=False)
    t.close()
    a, b, c = json.loads(calls[0].content)["batch"]
    assert a["event"] == "Error Caught" and a["userId"] == "u1"
    assert a["properties"]["type"] == "TypeError"
    assert a["properties"]["message"] == "order 41 for [email] failed with [key]"
    assert a["properties"]["route"] == "/checkout" and a["properties"]["handled"] is True
    assert "test_track.py" in a["properties"]["stack"] and "acme.example" not in a["properties"]["stack"]
    # The same bug with another number is the same issue; no one is "server".
    assert a["properties"]["fingerprint"] == b["properties"]["fingerprint"]
    assert b["anonymousId"] == "server"
    assert c["properties"] == {
        "type": "Error",
        "message": "plain text",
        "handled": False,
        "fingerprint": c["properties"]["fingerprint"],
    }
    # The hash the website script and the TypeScript library use.
    assert error_hash("TypeError|order 0 failed|") == "7cb8aa38c34b101a"


def test_crawler_visit_counts_crawlers_per_host_and_never_browsers():
    t, calls = tracker_with()
    gpt = "Mozilla/5.0 (compatible; GPTBot/1.2; +https://openai.com/gptbot)"
    assert t.crawler_visit(gpt, "WWW.example.com") is True
    assert t.crawler_visit(gpt, "www.example.com") is True
    assert t.crawler_visit("Mozilla/5.0 ChatGPT-User/1.0", "docs.example.com") is True
    assert t.crawler_visit("Mozilla/5.0 (Macintosh) Chrome/130 Safari/537.36", "www.example.com") is False
    assert t.crawler_visit(gpt, "") is False
    assert t.crawler_visit(None, "www.example.com") is False
    assert calls == []
    t.close()
    assert len(calls) == 1
    req = calls[0]
    assert str(req.url) == "http://test/v1/brands/northpeak/ai-bot-hits"
    assert req.headers["authorization"] == "Bearer oft_test_not_a_real_token"
    assert req.headers["idempotency-key"]
    assert json.loads(req.content)["hits"] == [
        {"host": "www.example.com", "user_agent": gpt, "requests": 2},
        {"host": "docs.example.com", "user_agent": "Mozilla/5.0 ChatGPT-User/1.0", "requests": 1},
    ]
    assert t.crawler_visit(gpt, "www.example.com") is False
    for ua in ["Amzn-User/1.0", "Google-Extended", "meta-webindexer/1.1", "Claude-User/1.0", "CCBot/2.0"]:
        assert looks_like_crawler(ua), ua
