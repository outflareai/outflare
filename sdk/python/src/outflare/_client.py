"""The HTTP side of the client: auth, retries, idempotency, paging and errors."""

from __future__ import annotations

import email.utils
import os
import random
import time
import uuid
from collections.abc import Iterator, Mapping
from typing import Any, Callable
from urllib.parse import quote

import httpx

from ._errors import OutflareError
from ._version import __version__

# Answers worth retrying: rate limits and a busy or restarting server.
_RETRY_STATUSES = {429, 502, 503, 504}


class _Base:
    """Settings and the request loop the generated methods call."""

    def __init__(
        self,
        token: str | None = None,
        *,
        base_url: str | None = None,
        workspace: str | None = None,
        agency: str | None = None,
        mission: str | None = None,
        max_retries: int = 3,
        timeout: float = 60.0,
        http_client: httpx.Client | None = None,
    ) -> None:
        self.token = token or os.environ.get("OUTFLARE_TOKEN")
        if not self.token:
            raise ValueError("pass token= or set OUTFLARE_TOKEN to an API token (oft_...)")
        self.base_url = (base_url or os.environ.get("OUTFLARE_URL") or "https://app.outflare.ai").rstrip("/")
        self._workspace = workspace or os.environ.get("OUTFLARE_WORKSPACE") or None
        self._agency = agency or os.environ.get("OUTFLARE_AGENCY") or None
        self.mission = mission
        self.max_retries = max_retries
        self.timeout = timeout
        self._http = http_client or httpx.Client(timeout=timeout)

    # ---- Where calls go ----

    @property
    def workspace(self) -> str:
        """The workspace calls go to: the one given, or else the token's own, from whoami."""
        if not self._workspace:
            token = self._call("GET", "/v1/whoami").get("token") or {}
            self._workspace = token.get("workspace_id")
            if not self._workspace:
                raise ValueError("this token has no workspace of its own (an agency token): pass workspace=")
        return self._workspace

    @property
    def agency(self) -> str:
        """The agency agency calls go to: the one given, or else the token's own, from whoami."""
        if not self._agency:
            token = self._call("GET", "/v1/whoami").get("token") or {}
            self._agency = token.get("agency_id")
            if not self._agency:
                raise ValueError("this is not an agency token: pass agency=")
        return self._agency

    def with_options(self, **changes: Any) -> Any:
        """A copy with some settings changed, sharing the connection pool.

        client.with_options(workspace="acme-client") or with_options(mission=mission_id)
        """
        opts: dict[str, Any] = {
            "token": self.token,
            "base_url": self.base_url,
            "workspace": self._workspace,
            "agency": self._agency,
            "mission": self.mission,
            "max_retries": self.max_retries,
            "timeout": self.timeout,
            "http_client": self._http,
        }
        opts.update(changes)
        return type(self)(**opts)

    def close(self) -> None:
        self._http.close()

    def __enter__(self) -> Any:
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    # ---- Paging ----

    def paginate(self, method: Callable[..., Any], **params: Any) -> Iterator[Any]:
        """Every item of a list, fetching page after page by next_cursor.

        for lead in client.paginate(client.list_leads, q="acme"):
            print(lead["email"])
        """
        params.pop("cursor", None)
        cursor = None
        while True:
            page = method(**params, **({"cursor": cursor} if cursor else {}))
            yield from page.get("data") or []
            cursor = page.get("next_cursor")
            if not cursor or page.get("has_more") is False:
                return

    # ---- Calls ----

    def request(
        self,
        method: str,
        route: str,
        *,
        path: Mapping[str, Any] | None = None,
        query: Mapping[str, Any] | None = None,
        body: Mapping[str, Any] | None = None,
        idempotency_key: str | None = None,
    ) -> Any:
        """Call any endpoint, including ones newer than this SDK.

        client.request("GET", "/v1/workspaces/{ws}/leads", query={"q": "acme"})
        """
        return self._call(method.upper(), route, path=path, query=query, body=body, idempotency_key=idempotency_key)

    def _call(
        self,
        method: str,
        route: str,
        *,
        path: Mapping[str, Any] | None = None,
        query: Mapping[str, Any] | None = None,
        body: Mapping[str, Any] | None = None,
        extra_body: Mapping[str, Any] | None = None,
        content: str | bytes | None = None,
        content_type: str | None = None,
        idempotency_key: str | None = None,
    ) -> Any:
        url = self.base_url + self._fill(route, dict(path or {}))
        params = {k: _query_value(v) for k, v in (query or {}).items() if v is not None}
        headers = {
            "Authorization": f"Bearer {self.token}",
            "Accept": "application/json",
            "User-Agent": f"outflare-python/{__version__}",
        }
        if self.mission:
            headers["Outflare-Mission"] = self.mission
        json_body: dict[str, Any] | None = None
        if body is not None or extra_body:
            json_body = {k: v for k, v in (body or {}).items() if v is not None}
            json_body.update(extra_body or {})
        if content is not None:
            headers["Content-Type"] = content_type or "application/octet-stream"
        # Every write carries an Idempotency-Key, so a retry after a timeout
        # returns the first answer instead of doing the work twice.
        if method != "GET":
            headers["Idempotency-Key"] = idempotency_key or str(uuid.uuid4())

        attempt = 0
        while True:
            try:
                resp = self._http.request(method, url, params=params, json=json_body, content=content, headers=headers)
            except httpx.TransportError:
                if attempt >= self.max_retries:
                    raise
                attempt += 1
                time.sleep(_backoff(attempt, None))
                continue
            if resp.status_code in _RETRY_STATUSES and attempt < self.max_retries:
                attempt += 1
                time.sleep(_backoff(attempt, resp.headers.get("Retry-After")))
                continue
            return _result(resp)

    def _fill(self, route: str, path: dict[str, Any]) -> str:
        if "{ws}" in route and not path.get("ws"):
            path["ws"] = self.workspace
        if "{ag}" in route and not path.get("ag"):
            path["ag"] = self.agency
        for k, v in path.items():
            if v is None:
                raise ValueError(f"{k} is required")
            route = route.replace("{" + k + "}", quote(str(v), safe=""))
        return route


def _query_value(v: Any) -> Any:
    if isinstance(v, bool):
        return "true" if v else "false"
    if isinstance(v, (list, tuple)):
        return [str(x) for x in v]  # sent as repeated parameters
    return v


def _backoff(attempt: int, retry_after: str | None) -> float:
    """Seconds to wait: what Retry-After asks (up to a minute), else 0.5s doubling with jitter."""
    if retry_after:
        try:
            return min(float(retry_after), 60.0)
        except ValueError:
            # Retry-After can also be an HTTP date.
            try:
                when: float = email.utils.parsedate_to_datetime(retry_after).timestamp()
            except (TypeError, ValueError):
                pass
            else:
                return max(0.0, min(when - time.time(), 60.0))
    return 0.5 * 2.0 ** (attempt - 1) * (0.75 + random.random() / 2)


def _result(resp: httpx.Response) -> Any:
    if resp.is_success:
        if not resp.content:
            return None
        if resp.headers.get("Content-Type", "").startswith("application/json"):
            return resp.json()
        return resp.text
    raise OutflareError.from_response(resp)
