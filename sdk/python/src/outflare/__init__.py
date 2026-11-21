"""The Outflare API client for Python.

from outflare import Outflare

client = Outflare()  # reads OUTFLARE_TOKEN and OUTFLARE_URL
lead = client.upsert_lead(email="ana@acme-com.example", first_name="Ana")
for lead in client.paginate(client.list_leads, q="acme"):
    print(lead["email"])
"""

from typing import Any

from ._client import _Base
from ._errors import OutflareError, WebhookVerificationError
from ._operations import Operations
from ._version import __version__
from .flags import LocalFlags, decide_flag, decide_flags, flag_bucket, flag_device, flags_bootstrap_tag
from .track import Tracker, describe_error, error_hash, looks_like_crawler
from .webhooks import identify_signature, verify_webhook


class Outflare(Operations, _Base):
    """A client for one Outflare server.

    token defaults to OUTFLARE_TOKEN and base_url to OUTFLARE_URL (else
    https://app.outflare.ai). The workspace is the token's own unless you
    pass workspace= (an ID or slug) or set OUTFLARE_WORKSPACE. Every
    operation in the API is a method, named after its operationId:
    listBrandLeads is list_brand_leads. Writes carry an Idempotency-Key and are
    retried safely; rate limits and busy servers are retried with backoff,
    honouring Retry-After. Errors raise OutflareError.
    """


def is_held(answer: Any) -> bool:
    """Whether a write was held for a person instead of running.

    True for the action layer's answer, and for a mission's from before missions retired.
    """
    if not isinstance(answer, dict) or "approval_required" not in answer:
        return False
    return "action" in answer or "mission_action" in answer


__all__ = [
    "LocalFlags",
    "Outflare",
    "OutflareError",
    "Tracker",
    "WebhookVerificationError",
    "__version__",
    "decide_flag",
    "decide_flags",
    "describe_error",
    "error_hash",
    "flag_bucket",
    "flag_device",
    "flags_bootstrap_tag",
    "identify_signature",
    "is_held",
    "looks_like_crawler",
    "verify_webhook",
]
