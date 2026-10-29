import base64
import hashlib
import hmac
import json
import time

import httpx
import pytest

from outflare import Outflare, OutflareError, WebhookVerificationError, is_held, verify_webhook

WS = "11111111-1111-4111-8111-111111111111"


def client_with(handler, **kw):
    seen = []

    def record(request):
        seen.append(request)
        return handler(request, len(seen))

    http = httpx.Client(transport=httpx.MockTransport(record))
    return Outflare("oft_test", base_url="http://test", http_client=http, **kw), seen


def test_workspace_comes_from_whoami_once():
    def handler(req, n):
        if req.url.path == "/v1/whoami":
            return httpx.Response(200, json={"token": {"workspace_id": WS}})
        return httpx.Response(200, json={"data": [], "next_cursor": None})

    c, seen = client_with(handler)
    c.list_leads(q="acme")
    c.list_mailboxes()
    assert [r.url.path for r in seen] == ["/v1/whoami", f"/v1/workspaces/{WS}/leads", f"/v1/workspaces/{WS}/mailboxes"]
    assert seen[1].url.params["q"] == "acme"
    assert seen[1].headers["Authorization"] == "Bearer oft_test"
    assert seen[1].headers["User-Agent"].startswith("outflare-python/")


def test_writes_carry_one_idempotency_key_across_retries():
    def handler(req, n):
        if n == 1:
            return httpx.Response(503, headers={"Retry-After": "0"})
        return httpx.Response(201, json={"id": "l1", "email": "ana@acme-com.example"})

    c, seen = client_with(handler, workspace="acme")
    lead = c.upsert_lead(email="ana@acme-com.example", first_name="Ana", extra_body={"custom": {"tier": "a"}})
    assert lead["id"] == "l1"
    assert len(seen) == 2
    keys = {r.headers["Idempotency-Key"] for r in seen}
    assert len(keys) == 1 and len(next(iter(keys))) == 36
    sent = json.loads(seen[1].content)
    assert sent == {"email": "ana@acme-com.example", "first_name": "Ana", "custom": {"tier": "a"}}
    assert seen[0].url.path == "/v1/workspaces/acme/leads"


def test_errors_carry_code_param_and_request_id():
    def handler(req, n):
        return httpx.Response(
            400,
            json={
                "error": {
                    "code": "invalid_request",
                    "message": "url must be https",
                    "param": "url",
                    "request_id": "req_1",
                }
            },
        )

    c, _ = client_with(handler, workspace="acme")
    with pytest.raises(OutflareError) as e:
        c.create_webhook(url="http://x", events=["lead.replied"])
    assert (e.value.status, e.value.code, e.value.param, e.value.request_id) == (400, "invalid_request", "url", "req_1")
    assert "url must be https" in str(e.value)


def test_paginate_follows_next_cursor():
    pages = {None: (["a", "b"], "c2"), "c2": (["c"], None)}

    def handler(req, n):
        data, nxt = pages[req.url.params.get("cursor")]
        return httpx.Response(200, json={"data": data, "next_cursor": nxt})

    c, seen = client_with(handler, workspace="acme")
    assert list(c.paginate(c.list_leads, limit=2)) == ["a", "b", "c"]
    assert seen[1].url.params["cursor"] == "c2" and seen[1].url.params["limit"] == "2"


def test_csv_upload_and_mission_header():
    def handler(req, n):
        return httpx.Response(202, json={"mission_action": {"id": "x"}, "approval_required": True})

    c, seen = client_with(handler, workspace="acme")
    held = c.with_options(mission="m1").import_mailboxes(content="email,password\na@b-co.example,pw\n", pool="Warm")
    assert is_held(held)
    r = seen[0]
    assert r.headers["Content-Type"] == "text/csv" and r.headers["Outflare-Mission"] == "m1"
    assert r.url.params["pool"] == "Warm" and r.content.startswith(b"email,password")


def test_positional_path_params_and_keywords():
    def handler(req, n):
        return httpx.Response(200, json={"id": "abc"})

    c, seen = client_with(handler, workspace="acme")
    c.get_lead("abc")
    assert seen[0].url.path == "/v1/workspaces/acme/leads/abc"


def test_verify_webhook():
    secret = "whsec_" + base64.b64encode(b"k" * 24).decode()
    body = b'{"id":"evt_1","type":"lead.replied"}'
    ts = str(int(time.time()))
    sig = base64.b64encode(hmac.new(b"k" * 24, b"evt_1." + ts.encode() + b"." + body, hashlib.sha256).digest()).decode()
    headers = {"Webhook-Id": "evt_1", "Webhook-Timestamp": ts, "Webhook-Signature": "v1,bad v1," + sig}
    assert verify_webhook(body, headers, secret)["type"] == "lead.replied"
    with pytest.raises(WebhookVerificationError):
        verify_webhook(body + b" ", headers, secret)
    with pytest.raises(WebhookVerificationError):
        verify_webhook(body, headers, secret, now=time.time() + 3600)


def test_token_is_required(monkeypatch):
    monkeypatch.delenv("OUTFLARE_TOKEN", raising=False)
    with pytest.raises(ValueError):
        Outflare()


def test_flags_bootstrap_tag_is_escaped():
    from outflare import flags_bootstrap_tag

    tag = flags_bootstrap_tag(
        {
            "flags": {"hero": "faster", "chat": False},
            "payloads": {"hero": {"headline": "</script><b>Ship & go</b>"}},
            "reasons": {"hero": "match"},
        },
        "srv-4f1c2a9e",
    )
    start = '<script type="application/json" id="outflare-flags">'
    assert tag.startswith(start) and tag.endswith("</script>")
    inner = tag[len(start) : -len("</script>")]
    assert not any(c in inner for c in "<>&")
    assert json.loads(inner) == {
        "anonymousId": "srv-4f1c2a9e",
        "flags": {"hero": "faster", "chat": False},
        "payloads": {"hero": {"headline": "</script><b>Ship & go</b>"}},
    }
