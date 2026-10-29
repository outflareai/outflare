"""Handing your server's flag decisions to the page."""

import json
from collections.abc import Mapping
from typing import Any


def flags_bootstrap_tag(decisions: Mapping[str, Any], anonymous_id: str) -> str:
    """The tag that hands your server's flag decisions to the page.

    The Outflare site script then shows the visitor's version at once, with
    no request and no flash of the original. Decide with evaluate_flags for
    the visitor's id: the of_aid cookie when the browser has one, else an id
    you made (8 to 64 letters, digits and dashes), which the script then
    uses for this page. Put the tag anywhere in the page, before site.js
    runs. The JSON is escaped so content in payloads can't close the tag.
    """
    body = json.dumps(
        {"anonymousId": anonymous_id, "flags": decisions.get("flags", {}), "payloads": decisions.get("payloads") or {}},
        separators=(",", ":"),
    )
    for c in "<>&\u2028\u2029":
        body = body.replace(c, f"\\u{ord(c):04x}")
    return '<script type="application/json" id="outflare-flags">' + body + "</script>"
