from __future__ import annotations

from typing import Any

import httpx


class OutflareError(Exception):
    """An error answer from the API.

    status is the HTTP status, code the stable machine-readable code
    (GET /v1/error-codes lists them all), param the request field a 400 is
    about, and request_id what to quote when reporting a problem.
    """

    def __init__(
        self,
        message: str,
        *,
        status: int,
        code: str,
        param: str | None = None,
        request_id: str | None = None,
        body: Any = None,
    ) -> None:
        super().__init__(message)
        self.message = message
        self.status = status
        self.code = code
        self.param = param
        self.request_id = request_id
        self.body = body

    def __str__(self) -> str:
        s = f"{self.status} {self.code}: {self.message}"
        if self.request_id:
            s += f" (request {self.request_id})"
        return s

    @classmethod
    def from_response(cls, resp: httpx.Response) -> OutflareError:
        try:
            body = resp.json()
        except ValueError:
            body = None
        err = body.get("error") if isinstance(body, dict) else None
        if not isinstance(err, dict):
            err = {}
        return cls(
            err.get("message") or resp.reason_phrase or "HTTP error",
            status=resp.status_code,
            code=err.get("code") or "http_error",
            param=err.get("param"),
            request_id=err.get("request_id") or resp.headers.get("X-Request-Id"),
            body=body,
        )


class WebhookVerificationError(Exception):
    """A webhook delivery whose signature or timestamp did not check out."""
