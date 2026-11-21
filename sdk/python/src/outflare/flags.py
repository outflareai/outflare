"""Flags on your server: deciding them in memory, and handing the decisions to the page."""

from __future__ import annotations

import hashlib
import json
import re
import threading
import time
from collections.abc import Callable, Mapping, Sequence
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


# ---- Deciding flags in your own server's memory ----
#
# evaluate_flags asks Outflare for every visitor. LocalFlags instead reads
# the brand's flag definitions (GET .../flags/definitions) every half
# minute and decides each visitor here, with no request per page: the same
# rules and the same hash as the server and the website script, so all
# three give a visitor the same version. The decisions are checked against
# the engine's own (engine/internal/flags/testdata).

_BUCKETS = 10_000


def flag_bucket(s: str) -> int:
    """Where s falls in 0..9999: the first 8 bytes of its sha256, mod 10,000."""
    return int.from_bytes(hashlib.sha256(s.encode("utf-8")).digest()[:8], "big") % _BUCKETS


_HEX = re.compile(r"%(?![0-9a-fA-F]{2})")
_PATH_OK = re.compile(r"^[A-Za-z0-9\-_.~$&+,/:;=@!'()*\[\]%]*$")
_PATH_KEEP = frozenset(b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~$&+,/:;=@")
_SCHEME = re.compile(r"^[a-zA-Z][a-zA-Z0-9+.\-]*:")


def _valid_escapes(s: str) -> bool:
    return _HEX.search(s) is None


def _percent_decode(s: str, plus_is_space: bool) -> bytes:
    raw = s.encode("utf-8")
    out = bytearray()
    i = 0
    while i < len(raw):
        c = raw[i]
        if c == 0x25:
            out.append(int(raw[i + 1 : i + 3], 16))
            i += 3
            continue
        out.append(0x20 if c == 0x2B and plus_is_space else c)
        i += 1
    return bytes(out)


def _escaped_path(raw: str) -> str:
    """The path as the server compares it: as written when that is a valid encoding, else encoded again."""
    if _PATH_OK.match(raw):
        return raw
    return "".join(chr(b) if b in _PATH_KEEP else f"%{b:02X}" for b in _percent_decode(raw, False))


def _parse_query(raw: str) -> dict[str, str]:
    out: dict[str, str] = {}
    for part in raw.split("&"):
        if part == "" or ";" in part:
            continue
        k, _, v = part.partition("=")
        if not _valid_escapes(k) or not _valid_escapes(v):
            continue
        key = _percent_decode(k, True).decode("utf-8", "replace")
        # The first value of a repeated parameter is the one compared.
        out.setdefault(key, _percent_decode(v, True).decode("utf-8", "replace"))
    return out


def _parse_address(url: str) -> tuple[str, str, dict[str, str]] | None:
    """host, path and query of an address, read as the server reads it, or None when it is not one."""
    if url == "" or any(ord(c) < 0x20 or ord(c) == 0x7F for c in url):
        return None
    rest = url.split("#", 1)[0]
    scheme = _SCHEME.match(rest)
    if scheme:
        rest = rest[scheme.end() :]
    elif re.match(r"^[^/?]*:", rest):
        return None
    rest, _, raw_query = rest.partition("?")
    if scheme and rest != "" and not rest.startswith("/"):
        # An address with a scheme and no // (mailto:someone) has no host or path to compare.
        return "", "", _parse_query(raw_query)
    host = ""
    if rest.startswith("//"):
        end = rest.find("/", 2)
        if end < 0:
            end = len(rest)
        authority, rest = rest[2:end], rest[end:]
        authority = authority.rsplit("@", 1)[-1]
        if authority.startswith("["):
            close = authority.find("]")
            if close < 0:
                return None
            port = authority[close + 1 :]
            if port != "" and not re.match(r"^:\d*$", port):
                return None
            host = authority[1:close]
        else:
            if ":" in authority:
                authority, _, port = authority.rpartition(":")
                if not re.match(r"^\d*$", port):
                    return None
            host = authority
        if not _valid_escapes(host):
            return None
    if not _valid_escapes(rest):
        return None
    host = host.lower()
    if host.startswith("www."):
        host = host[4:]
    return host, _escaped_path(rest), _parse_query(raw_query)


def _rule_holds(rule: Mapping[str, Any], v: Mapping[str, Any], address: tuple[str, str, dict[str, str]] | None) -> bool:
    attribute = rule.get("attribute", "")
    if attribute == "list":
        mine = [x.lower() for x in (v.get("lists") or [])]
        in_one = any(x.lower() in mine for x in (rule.get("values") or []))
        return in_one if rule.get("op") == "is" else (not in_one if rule.get("op") == "is_not" else False)
    val, present = "", False
    if attribute.startswith("query."):
        query = address[2] if address else {}
        name = attribute[6:]
        present = name in query
        val = query.get(name, "")
    elif attribute.startswith("property."):
        props = v.get("properties") or {}
        name = attribute[9:]
        present = name in props
        val = props.get(name, "")
    elif attribute.startswith("person."):
        person = v.get("person") or {}
        name = attribute[7:]
        present = name in person
        val = person.get(name, "")
    elif attribute == "signed_in":
        val, present = ("true" if v.get("signed_in") else "false"), True
    else:
        if attribute == "host":
            val = address[0] if address else ""
        elif attribute == "path":
            val = address[1] if address else ""
        elif attribute in ("url", "referrer", "user_id"):
            val = v.get(attribute) or ""
        elif attribute in ("device", "language"):
            val = (v.get(attribute) or "").lower()
        present = val != ""
    op = rule.get("op")
    if op == "exists":
        return present
    if op == "not_exists":
        return not present
    val = val.lower()
    values = [x.lower() for x in (rule.get("values") or [])]
    if op == "is":
        return present and val in values
    if op == "is_not":
        return val not in values
    if op == "contains":
        return present and any(x in val for x in values)
    if op == "not_contains":
        return not any(x in val for x in values)
    if op == "starts_with":
        return present and any(val.startswith(x) for x in values)
    if op == "ends_with":
        return present and any(val.endswith(x) for x in values)
    return False


def decide_flag(flag: Mapping[str, Any], visitor: Mapping[str, Any]) -> dict[str, Any]:
    """Decide one flag for a visitor, as Outflare's server does.

    Most code wants decide_flags or LocalFlags; this is the core they
    share, public so its answers can be tested against the server's.
    visitor has subject, signed_in, url, referrer, device, language and
    properties (text values). Returns variant ("" for none), payload and
    reason (match, rules, group, rollout, off, no_subject).
    """
    variants = flag.get("variants") or []
    if not variants:
        return {"variant": "", "payload": None, "reason": "off"}
    subject = visitor.get("subject") or ""
    if not subject:
        return {"variant": "", "payload": None, "reason": "no_subject"}
    address = _parse_address(visitor.get("url") or "")
    for rule in flag.get("rules") or []:
        if not _rule_holds(rule, visitor, address):
            return {"variant": "", "payload": None, "reason": "rules"}
    if flag.get("group"):
        g = flag_bucket("group:" + flag["group"] + ":" + subject)
        if g < flag.get("group_from", 0) or g >= flag.get("group_to", 0):
            return {"variant": "", "payload": None, "reason": "group"}
    salt = flag.get("salt", "")
    if flag_bucket(salt + ":rollout:" + subject) >= flag.get("rollout", 0) * 100:
        return {"variant": "", "payload": None, "reason": "rollout"}
    b = flag_bucket(salt + ":" + subject)
    total = sum(x.get("weight", 0) for x in variants)
    if total == 0:
        return {"variant": "", "payload": None, "reason": "off"}
    at = 0
    for i, x in enumerate(variants):
        at += x.get("weight", 0)
        if b * total < at * _BUCKETS or i == len(variants) - 1:
            return {"variant": x["key"], "payload": x.get("payload"), "reason": "match"}
    return {"variant": "", "payload": None, "reason": "off"}


def flag_device(user_agent: str | None) -> str:
    """desktop, mobile or tablet from a User-Agent header, or "" when it names no system: what device rules compare."""
    ua = user_agent or ""

    def has(*names: str) -> bool:
        return any(n in ua for n in names)

    apple = has("iPhone", "iPad", "iPod")
    android = not apple and has("Android")
    known = apple or android or has("Windows", "CrOS", "Macintosh", "Mac OS X", "Linux")
    if has("iPad", "Tablet") or (android and not has("Mobile")):
        return "tablet"
    if has("Mobi", "iPhone", "iPod"):
        return "mobile"
    return "desktop" if known else ""


def _clip(s: str, n: int) -> str:
    raw = s.encode("utf-8")
    if len(raw) <= n:
        return s
    while n > 0 and raw[n] & 0xC0 == 0x80:
        n -= 1
    return raw[:n].decode("utf-8", "ignore")


def decide_flags(
    definitions: Mapping[str, Any] | Sequence[Mapping[str, Any]],
    *,
    anonymous_id: str | None = None,
    user_id: str | None = None,
    url: str | None = None,
    referrer: str | None = None,
    user_agent: str | None = None,
    language: str | None = None,
    properties: Mapping[str, Any] | None = None,
    keys: Sequence[str] | None = None,
    lists: Sequence[str] | None = None,
    person: Mapping[str, Any] | None = None,
) -> dict[str, Any]:
    """Decide every flag in definitions for a visitor, in memory.

    lists (ids of the People lists the visitor is in) and person (fields
    of their record) feed list and person.<field> rules: deciding in
    memory can't look a person up, so pass what your server knows.

    The answer evaluate_flags would give, with no request: flags (each
    flag's version, or False), payloads and reasons. definitions is what
    get_flag_definitions returns, or its "flags" list.
    """
    flags = definitions.get("flags", []) if isinstance(definitions, Mapping) else definitions
    user = (user_id or "").strip()
    subject = (anonymous_id or "").strip() or user
    if not subject or len(subject) > 200:
        raise ValueError(
            "give anonymous_id (outflare.anonymousId() from the page, so the version matches the site's) "
            "or user_id, up to 200 characters"
        )

    def texts(values: Mapping[str, Any] | None) -> dict[str, str]:
        out: dict[str, str] = {}
        for k, x in (values or {}).items():
            if x is None:
                continue
            text = x if isinstance(x, str) else json.dumps(x, separators=(",", ":"), ensure_ascii=False).strip('"')
            out[k] = _clip(text, 500)
        return out

    props = texts(properties)
    visitor = {
        "subject": subject,
        "signed_in": user != "",
        "url": _clip(url or "", 2000),
        "referrer": _clip(referrer or "", 2000),
        "device": flag_device(user_agent),
        "language": _clip(language or "", 35),
        "properties": props,
        "user_id": user,
        "lists": list(lists or []),
        "person": texts(person),
    }
    out: dict[str, Any] = {"flags": {}, "payloads": {}, "reasons": {}}
    for f in flags:
        if keys and f["key"] not in keys:
            continue
        res = decide_flag(f, visitor)
        out["flags"][f["key"]] = res["variant"] or False
        if res["variant"] and res["payload"] is not None:
            out["payloads"][f["key"]] = res["payload"]
        out["reasons"][f["key"]] = res["reason"]
    return out


class LocalFlags:
    """Decides flags in your server's memory.

    It reads the brand's flag definitions on the first decide, then again
    when they are older than the refresh time (half a minute), so a page
    costs no request to Outflare. When a refresh fails, deciding carries on
    with the last definitions and tries again after the refresh time, so a
    slow or unreachable Outflare never fails a page. A change to a flag
    reaches your server within the refresh time.

        flags = LocalFlags(Outflare(), brand=brand_id)
        decisions = flags.decide(anonymous_id=aid, url=request.url, user_agent=ua)
        html = html.replace("</head>", flags_bootstrap_tag(decisions, aid) + "</head>")

    The token needs analytics.read and stays on your server. Safe to share
    between threads.
    """

    def __init__(
        self,
        client: Any,
        *,
        brand: str | None = None,
        refresh_seconds: float | None = None,
        on_error: Callable[[Exception], None] | None = None,
    ) -> None:
        self._client = client
        self._brand = brand
        self._refresh_seconds = refresh_seconds
        self._on_error = on_error
        self._definitions: dict[str, Any] | None = None
        self._loaded_at = 0.0
        self._lock = threading.Lock()

    def current(self) -> dict[str, Any] | None:
        """The definitions in memory, or None before the first load."""
        return self._definitions

    def refresh(self) -> None:
        """Read the definitions now. Raises when the read fails; what is in memory stays."""
        data = self._client.request("GET", "/v1/brands/{brand}/flags/definitions", path={"brand": self._brand})
        if not isinstance(data, dict) or not isinstance(data.get("flags"), list):
            raise ValueError("reading flag definitions gave no flags")
        self._definitions = data
        self._loaded_at = time.monotonic()

    def _stale(self) -> bool:
        every = self._refresh_seconds
        if every is None:
            every = (self._definitions or {}).get("refresh_seconds", 30)
        return time.monotonic() - self._loaded_at >= max(5, every)

    def decide(self, **visitor: Any) -> dict[str, Any]:
        """The visitor's flags: the keywords of decide_flags (anonymous_id, user_id, url, ...)."""
        if self._definitions is None:
            with self._lock:
                if self._definitions is None:
                    self.refresh()
        elif self._stale() and self._lock.acquire(blocking=False):
            # One thread refreshes; the others answer from memory meanwhile.
            try:
                self.refresh()
            except Exception as err:
                # Try again after the refresh time, not on every page.
                self._loaded_at = time.monotonic()
                if self._on_error:
                    self._on_error(err)
            finally:
                self._lock.release()
        return decide_flags(self._definitions or {}, **visitor)

    def variant(self, key: str, **visitor: Any) -> str | bool:
        """One flag's version for the visitor: its variant's key, or False when the flag is off for them."""
        return self.decide(**{**visitor, "keys": [key]})["flags"].get(key) or False

    def is_enabled(self, key: str, **visitor: Any) -> bool:
        """Whether a flag is on for the visitor, whichever version they get."""
        return self.variant(key, **visitor) is not False

    def payload(self, key: str, **visitor: Any) -> Any:
        """The content of the visitor's version of a flag, or None when it has none or the flag is off for them."""
        d = self.decide(**{**visitor, "keys": [key]})
        return d["payloads"].get(key) if d["flags"].get(key) else None
