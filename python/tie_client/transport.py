"""Daemon (triple-store) transport.

Every operation is ``POST {webservice}/{Id}`` with a JSON body and HTTP Basic
Auth, mirroring webservice/client.go. Transport-level failures are retried up
to 4 times with exponential backoff; an HTTP 401 raises Unauthorized without
retry; any returned response (even a logical failure) is not retried. Dump is
streamed as NDJSON via ``run_stream``.
"""

from __future__ import annotations

import json
import ssl
import time
import urllib.error
import urllib.request
from base64 import b64encode
from collections.abc import Iterator
from typing import Any

from .errors import Unauthorized

MAX_ATTEMPTS = 4


def _backoff(attempt: int) -> float:
    """Pause before attempt n (1-indexed): 100ms, 200ms, 400ms, ..."""
    return 0.1 * (1 << (attempt - 1))


class DaemonClient:
    def __init__(self, server: str, username: str, password: str, insecure: bool = False):
        self.server = server.rstrip("/")
        self._auth = "Basic " + b64encode(f"{username}:{password}".encode()).decode()
        self._ctx: ssl.SSLContext | None = None
        if server.startswith("https") and insecure:
            self._ctx = ssl.create_default_context()
            self._ctx.check_hostname = False
            self._ctx.verify_mode = ssl.CERT_NONE

    def _request(self, request_id: str, body: dict) -> urllib.request.Request:
        data = json.dumps(body).encode()
        req = urllib.request.Request(
            f"{self.server}/{request_id}", data=data, method="POST"
        )
        req.add_header("Content-Type", "application/json")
        req.add_header("Authorization", self._auth)
        return req

    def run(self, request_id: str, body: dict) -> dict[str, Any]:
        """POST a request and return the parsed JSON reply object.

        Retries only transport-level failures. HTTP 401 raises Unauthorized.
        """
        last_err: Exception | None = None
        for attempt in range(1, MAX_ATTEMPTS + 1):
            if attempt > 1:
                time.sleep(_backoff(attempt - 1))
            req = self._request(request_id, body)
            try:
                with urllib.request.urlopen(req, context=self._ctx) as resp:
                    raw = resp.read()
            except urllib.error.HTTPError as e:
                if e.code == 401:
                    raise Unauthorized("Unauthorized") from None
                # A returned response (non-2xx that is not 401) means the server
                # was reached; do not retry. Try to parse its body as a reply.
                raw = e.read()
                return json.loads(raw) if raw else {}
            except urllib.error.URLError as e:
                last_err = e
                continue
            return json.loads(raw) if raw else {}
        raise last_err if last_err else RuntimeError("request failed")

    def run_stream(self, request_id: str, body: dict) -> Iterator[dict]:
        """POST a request and yield each NDJSON reply object (for Dump).

        The connection attempt is retried; a mid-stream failure surfaces while
        iterating. HTTP 401 raises Unauthorized.
        """
        last_err: Exception | None = None
        resp = None
        for attempt in range(1, MAX_ATTEMPTS + 1):
            if attempt > 1:
                time.sleep(_backoff(attempt - 1))
            req = self._request(request_id, body)
            try:
                resp = urllib.request.urlopen(req, context=self._ctx)
                break
            except urllib.error.HTTPError as e:
                if e.code == 401:
                    raise Unauthorized("Unauthorized") from None
                raise RuntimeError(f"stream request failed: {e.code} {e.reason}") from None
            except urllib.error.URLError as e:
                last_err = e
                resp = None
                continue
        if resp is None:
            raise last_err if last_err else RuntimeError("stream request failed")
        with resp:
            for line in resp:
                line = line.strip()
                if line:
                    yield json.loads(line)
