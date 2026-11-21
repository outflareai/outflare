"""Sending product events from a server.

from outflare import Tracker

tracker = Tracker(brand="northpeak")  # reads OUTFLARE_TOKEN and OUTFLARE_URL
tracker.identify("u_123", {"email": "ana@acme.example", "plan": "pro"})
tracker.track("u_123", "Order Completed", {"total": 42, "currency": "USD"})
tracker.close()  # before the process exits

Calls are queued in memory and sent in batches to POST
/v1/brands/{brand}/events by a background thread, with retries.
"""

from __future__ import annotations

import atexit
import os
import re
import threading
import time
import traceback
import uuid
from datetime import datetime, timezone
from typing import Any, Callable
from urllib.parse import quote

import httpx

from ._version import __version__

_MAX_BATCH = 500


_EMAIL = re.compile(r"""[^\s@"'<>()]+@[^\s@"'<>()]+\.[A-Za-z]{2,}""")
_KEY = re.compile(r"[A-Za-z0-9_\-]{32,}")
_UUID = re.compile(r"[0-9a-f]{8}-[0-9a-f-]{27}", re.IGNORECASE)
_CRAWLER = re.compile(
    r"bot|crawl|spider|gpt|claude|anthropic|openai|perplexity|cohere|bytespider|duckassist|mistral"
    r"|meta-external|webindexer|amzn|google-extended|ai2|diffbot|scrapy|fetcher|agent\b",
    re.IGNORECASE,
)


def looks_like_crawler(user_agent: str | None) -> bool:
    """Whether a user agent may be a crawler: a cheap first look, so a busy
    server keeps no count for ordinary browsers. Outflare decides which of
    them are AI crawlers."""
    return bool(user_agent) and _CRAWLER.search(user_agent) is not None


def _scrub(text: Any, limit: int) -> str:
    return _KEY.sub("[key]", _EMAIL.sub("[email]", "" if text is None else str(text)))[:limit]


def error_hash(text: str) -> str:
    """The website script's hash, so the same bug groups alike wherever it was caught."""
    h1, h2 = 5381, 52711
    data = text.encode("utf-16-le", "surrogatepass")
    for i in range(0, len(data), 2):
        c = data[i] | (data[i + 1] << 8)
        h1 = ((h1 * 33) ^ c) & 0xFFFFFFFF
        h2 = ((h2 * 33) ^ c) & 0xFFFFFFFF
    return f"{h1:08x}{h2:08x}"


def describe_error(error: BaseException | str, handled: bool = True) -> dict[str, Any]:
    """What an error is sent as: type, message and stack without email addresses or key-shaped strings.

    The fingerprint stays the same when only numbers, ids or line numbers change.
    """
    if isinstance(error, BaseException):
        kind = _scrub(type(error).__name__, 100)
        message = _scrub(error, 500)
        stack = _scrub("".join(traceback.format_exception(type(error), error, error.__traceback__)), 4000)
        frames = traceback.extract_tb(error.__traceback__)
        top = f"{frames[-1].filename}:{frames[-1].name}" if frames else ""
    else:
        kind, message, stack, top = "Error", _scrub(error, 500), "", ""
    out: dict[str, Any] = {"type": kind, "message": message, "handled": handled}
    if stack:
        out["stack"] = stack
    out["fingerprint"] = error_hash(f"{kind}|{_UUID.sub('id', re.sub(r'[0-9]+', '0', message))}|{top}")
    return out


class Tracker:
    """A queue of product events for one brand. Make one and share it.

    token is an API token with customers.manage (default OUTFLARE_TOKEN);
    keep it on your servers. Events are sent once flush_at wait or every
    flush_interval seconds, whichever comes first. A batch the server
    cannot take is tried max_retries more times; a refusal (a bad token,
    no such brand) is not. on_error(exception, lost) hears about batches
    that were given up on.
    """

    def __init__(
        self,
        brand: str,
        token: str | None = None,
        base_url: str | None = None,
        *,
        flush_at: int = 20,
        flush_interval: float = 10.0,
        max_queue: int = 10000,
        max_retries: int = 3,
        on_error: Callable[[Exception, int], None] | None = None,
        transport: httpx.BaseTransport | None = None,
    ) -> None:
        token = token or os.environ.get("OUTFLARE_TOKEN")
        if not token:
            raise ValueError("Tracker needs a token: pass token= or set OUTFLARE_TOKEN")
        if not brand:
            raise ValueError("Tracker needs a brand: its id or slug")
        base = (base_url or os.environ.get("OUTFLARE_URL") or "https://app.outflare.ai").rstrip("/")
        self._url = f"{base}/v1/brands/{quote(brand, safe='')}/events"
        self._crawler_url = f"{base}/v1/brands/{quote(brand, safe='')}/ai-bot-hits"
        self._crawlers: dict[tuple[str, str], int] = {}
        self._http = httpx.Client(
            timeout=10.0,
            transport=transport,
            headers={"Authorization": f"Bearer {token}", "User-Agent": f"outflare-python/{__version__}"},
        )
        self._flush_at = min(max(flush_at, 1), _MAX_BATCH)
        self._flush_interval = flush_interval
        self._max_queue = max_queue
        self._max_retries = max_retries
        self._on_error = on_error
        self._queue: list[dict[str, Any]] = []
        self._lock = threading.Lock()
        self._send_lock = threading.Lock()
        self._wake = threading.Event()
        self._closed = False
        self._thread = threading.Thread(target=self._run, name="outflare-tracker", daemon=True)
        self._thread.start()
        atexit.register(self.close)

    def track(self, user_id: str | None, event: str, properties: dict[str, Any] | None = None, **kw: Any) -> str:
        """Something a person did."""
        if not event:
            raise ValueError("track needs an event name")
        return self._add("track", user_id, {"event": event, "properties": properties or {}}, kw)

    def identify(self, user_id: str, traits: dict[str, Any] | None = None, **kw: Any) -> str:
        """Who a person is."""
        if not user_id:
            raise ValueError("identify needs a user_id")
        return self._add("identify", user_id, {"traits": traits or {}}, kw)

    def group(self, user_id: str | None, group_id: str, traits: dict[str, Any] | None = None, **kw: Any) -> str:
        """The company a person belongs to."""
        if not group_id:
            raise ValueError("group needs a group_id")
        return self._add("group", user_id, {"groupId": group_id, "traits": traits or {}}, kw)

    def page(
        self, user_id: str | None, name: str | None = None, properties: dict[str, Any] | None = None, **kw: Any
    ) -> str:
        """A page someone saw."""
        return self._add("page", user_id, {"name": name, "properties": properties or {}}, kw)

    def screen(
        self, user_id: str | None, name: str | None = None, properties: dict[str, Any] | None = None, **kw: Any
    ) -> str:
        """A screen someone saw in an app."""
        return self._add("screen", user_id, {"name": name, "properties": properties or {}}, kw)

    def exposed(self, user_id: str | None, flag: str, variant: str | bool | None, **kw: Any) -> str | None:
        """A visitor was shown a flag's version: sends Experiment Viewed, which joins them to its A/B test.

        Call it when your server renders the version, not when it decides it,
        with the id the flags were decided for (anonymous_id=... for a
        visitor). Sends nothing for a flag that is off for them.
        """
        if not flag:
            raise ValueError("exposed needs the flag's key")
        if not variant:
            return None
        props = {"flag": flag, "variant": variant, "source": "server"}
        return self._add("track", user_id, {"event": "Experiment Viewed", "properties": props}, kw)

    def capture_exception(
        self,
        error: BaseException | str,
        user_id: str | None = None,
        properties: dict[str, Any] | None = None,
        handled: bool = True,
        **kw: Any,
    ) -> str:
        """An error your server caught: sends Error Caught, which the Errors page groups into issues.

        Email addresses and key-shaped strings are taken out of the message
        and stack first. Without a user_id or anonymous_id the error is about
        no one and is filed under the anonymous id "server".
        """
        if not user_id and not kw.get("anonymous_id"):
            kw["anonymous_id"] = "server"
        props = {**(properties or {}), **describe_error(error, handled)}
        return self._add("track", user_id, {"event": "Error Caught", "properties": props}, kw)

    def crawler_visit(self, user_agent: str | None, host: str | None) -> bool:
        """Count a request from an AI crawler (GPTBot, ClaudeBot, PerplexityBot
        and others), for a site that isn't behind a CDN Outflare reads logs from.

        Call it for every request from your server's middleware, with the
        request's User-Agent and Host. It returns at once, keeps a count in
        memory when the user agent looks like a crawler, and sends the counts
        with the next flush. Outflare decides which are AI crawlers and keeps
        only how often each came per host and day. Returns whether it counted.
        """
        ua = (user_agent or "")[:300]
        host = (host or "").strip().lower()
        if self._closed or not host or not looks_like_crawler(ua):
            return False
        with self._lock:
            self._crawlers[(host, ua)] = self._crawlers.get((host, ua), 0) + 1
            full = len(self._crawlers) >= 1000
        if full:
            self._wake.set()
        return True

    def alias(self, user_id: str, previous_id: str, **kw: Any) -> str:
        """An id a person had before: joins previous_id to user_id."""
        if not user_id or not previous_id:
            raise ValueError("alias needs a user_id and a previous_id")
        return self._add("alias", user_id, {"previousId": previous_id}, kw)

    def flush(self) -> None:
        """Send everything waiting now. Returns once it was sent or given up on."""
        with self._send_lock:
            with self._lock:
                crawlers, self._crawlers = self._crawlers, {}
            if crawlers:
                hits = [{"host": h, "user_agent": ua, "requests": n} for (h, ua), n in crawlers.items()]
                try:
                    # The key makes a retried report count once.
                    self._post(self._crawler_url, {"hits": hits}, {"Idempotency-Key": str(uuid.uuid4())})
                except Exception as err:
                    if self._on_error:
                        self._on_error(err, 0)
            while True:
                with self._lock:
                    batch, self._queue = self._queue[:_MAX_BATCH], self._queue[_MAX_BATCH:]
                if not batch:
                    return
                try:
                    self._send(batch)
                except Exception as err:
                    if self._on_error:
                        self._on_error(err, len(batch))

    def close(self) -> None:
        """Send what waits and take no more. Call it before the process exits."""
        if self._closed:
            return
        self._closed = True
        self._wake.set()
        self.flush()
        self._http.close()

    def _add(self, kind: str, user_id: str | None, rest: dict[str, Any], kw: dict[str, Any]) -> str:
        if self._closed:
            raise RuntimeError("the tracker is closed")
        anonymous_id = kw.pop("anonymous_id", None)
        timestamp = kw.pop("timestamp", None)
        message_id = kw.pop("message_id", None) or str(uuid.uuid4())
        context = dict(kw.pop("context", None) or {})
        if kw:
            raise TypeError(f"unexpected arguments: {', '.join(sorted(kw))}")
        if not user_id and not anonymous_id:
            raise ValueError(f"{kind} needs a user_id or an anonymous_id")
        if timestamp is None:
            timestamp = datetime.now(timezone.utc)
        if isinstance(timestamp, datetime):
            if timestamp.tzinfo is None:
                timestamp = timestamp.replace(tzinfo=timezone.utc)
            timestamp = timestamp.isoformat()
        context["library"] = {"name": "outflare-python", "version": __version__}
        message: dict[str, Any] = {
            "type": kind,
            "messageId": message_id,
            "timestamp": timestamp,
            "context": context,
            **rest,
        }
        if user_id:
            message["userId"] = user_id
        if anonymous_id:
            message["anonymousId"] = anonymous_id
        lost = 0
        with self._lock:
            self._queue.append(message)
            if len(self._queue) > self._max_queue:
                lost = len(self._queue) - self._max_queue
                del self._queue[:lost]
            full = len(self._queue) >= self._flush_at
        if lost and self._on_error:
            self._on_error(RuntimeError("the queue is full, so the oldest events were dropped"), lost)
        if full:
            self._wake.set()
        return message_id

    def _run(self) -> None:
        while not self._closed:
            self._wake.wait(self._flush_interval)
            self._wake.clear()
            if not self._closed:
                self.flush()

    def _send(self, batch: list[dict[str, Any]]) -> None:
        self._post(self._url, {"source": "python", "batch": batch}, None)

    def _post(self, url: str, body: dict[str, Any], headers: dict[str, str] | None) -> None:
        attempt = 0
        while True:
            status, retry_after = 0, 0.0
            try:
                res = self._http.post(url, json=body, headers=headers)
                status = res.status_code
                if res.is_success:
                    return
                try:
                    retry_after = float(res.headers.get("retry-after", 0))
                except ValueError:
                    retry_after = 0.0
            except httpx.HTTPError:
                if attempt >= self._max_retries:
                    raise
            if 400 <= status < 500 and status not in (408, 429):
                raise RuntimeError(f"the server refused the batch ({status})")
            if attempt >= self._max_retries:
                raise RuntimeError(f"the server could not take the batch ({status or 'no answer'})")
            time.sleep(min(retry_after or 0.5 * 2**attempt, 30.0))
            attempt += 1
