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
from .flags import flags_bootstrap_tag
from .webhooks import verify_webhook


class Outflare(Operations, _Base):
    """A client for one Outflare server.

    token defaults to OUTFLARE_TOKEN and base_url to OUTFLARE_URL (else
    https://app.outflare.ai). The workspace is the token's own unless you
    pass workspace= (an ID or slug) or set OUTFLARE_WORKSPACE. Every
    operation in the API is a method, named after its operationId:
    listLeads is list_leads. Writes carry an Idempotency-Key and are
    retried safely; rate limits and busy servers are retried with backoff,
    honouring Retry-After. Errors raise OutflareError.
    """


def is_held(answer: Any) -> bool:
    """Whether a write made inside a mission was held (a dry run, or waiting for approval) instead of running."""
    return isinstance(answer, dict) and "mission_action" in answer and "approval_required" in answer


__all__ = [
    "Outflare",
    "OutflareError",
    "WebhookVerificationError",
    "__version__",
    "flags_bootstrap_tag",
    "is_held",
    "verify_webhook",
]
