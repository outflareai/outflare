"""Checking webhook deliveries from Outflare (Standard Webhooks signatures)."""

from __future__ import annotations

import base64
import hashlib
import hmac
import json
import time
from collections.abc import Mapping
from typing import Any

from ._errors import WebhookVerificationError


def verify_webhook(
    payload: str | bytes,
    headers: Mapping[str, str],
    secret: str,
    *,
    tolerance: int = 300,
    now: float | None = None,
) -> Any:
    """Check a delivery and return its event.

    Pass the raw request body (before any JSON parsing), the request headers
    and the endpoint's signing secret (whsec_...). Raises
    WebhookVerificationError if no signature matches or the delivery is more
    than five minutes old or early. Use event["id"] to skip a delivery you
    already handled.

        event = verify_webhook(request.body, request.headers, os.environ["OUTFLARE_WEBHOOK_SECRET"])
        if event["type"] == "lead.replied":
            ...
    """
    h = {k.lower(): v for k, v in headers.items()}
    msg_id, ts, sigs = h.get("webhook-id"), h.get("webhook-timestamp"), h.get("webhook-signature")
    if not msg_id or not ts or not sigs:
        raise WebhookVerificationError("missing webhook-id, webhook-timestamp or webhook-signature header")
    try:
        sent = int(ts)
    except ValueError:
        raise WebhookVerificationError("webhook-timestamp is not a number") from None
    if abs((now if now is not None else time.time()) - sent) > tolerance:
        raise WebhookVerificationError("webhook-timestamp is too far from now")
    raw = payload.encode() if isinstance(payload, str) else payload
    try:
        key = base64.b64decode(secret.removeprefix("whsec_"))
    except ValueError:
        raise WebhookVerificationError("the secret is not a whsec_ signing secret") from None
    expected = hmac.new(key, msg_id.encode() + b"." + ts.encode() + b"." + raw, hashlib.sha256).digest()
    for part in sigs.split(" "):
        version, _, sig = part.partition(",")
        if version != "v1" or not sig:
            continue
        try:
            given = base64.b64decode(sig)
        except ValueError:
            continue
        if hmac.compare_digest(given, expected):
            return json.loads(raw)
    raise WebhookVerificationError("no signature matches the secret")
