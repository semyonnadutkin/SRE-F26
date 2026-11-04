#!/usr/bin/env python3
"""Standalone FileServer prober"""

import argparse
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
import hashlib
from http.client import HTTPException
from http.cookiejar import CookieJar
import json
import os
import signal
import threading
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit
from urllib.request import HTTPCookieProcessor, ProxyHandler, Request, build_opener
import uuid


PUBLIC_PATHS = (
    "/", "/index.html", "/app.js", "/style.css", "/favicon.svg",
    "/403.html", "/403.css", "/404.html", "/404.css",
    "/healthz", "/readyz", "/metrics",
)


class ProbeError(Exception):
    pass


class Stopped(Exception):
    pass


def target_url(value):
    if "://" not in value:
        value = "http://" + value
    parsed = urlsplit(value)
    if (parsed.scheme not in ("http", "https") or not parsed.hostname
            or parsed.username or parsed.password or parsed.path not in ("", "/")
            or parsed.query or parsed.fragment):
        raise ValueError("TARGET_URL must be an HTTP(S) origin, e.g. 127.0.0.1:80")
    _ = parsed.port  # Validate the port before starting workers.
    return value.rstrip("/")


class Prober:
    def __init__(self, target, mode):
        self.target = target
        self.workers = 16 if mode == "stress" else 1
        self.delay = 0 if mode == "stress" else 1
        # One immutable file shared by all workers and all upload requests.
        self.file = os.urandom(8 * 1024 * 1024 if mode == "stress" else 4096)
        self.digest = hashlib.sha256(self.file).digest()
        self.stop = threading.Event()
        self.lock = threading.Lock()
        self.counts = Counter()
        self.in_flight = 0

    def record(self, key):
        with self.lock:
            self.counts[key] += 1

    def request(self, opener, method, path, expected=200, body=None,
                decode=False, download=False, cleanup=False):
        if self.stop.is_set() and not cleanup:
            raise Stopped
        expected = (expected,) if isinstance(expected, int) else expected
        headers = {"Origin": self.target}
        if isinstance(body, dict):
            body = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        elif body is not None:
            headers["Content-Type"] = "application/octet-stream"
        request = Request(self.target + path, data=body, headers=headers, method=method)
        # Collapse IDs and query strings in diagnostics.
        route = path.split("?")[0]
        parts = route.split("/")
        if len(parts) > 3 and parts[2] == "files":
            parts[3] = "{id}"
        label = method + " " + "/".join(parts)
        with self.lock:
            self.in_flight += 1
        try:
            try:
                response = opener.open(request, timeout=15)
            except HTTPError as error:
                if error.code not in expected:
                    raise
                response = error
            with response:
                status = response.status
                payload = response.read() if decode else None
                digest = hashlib.sha256()
                if not decode:
                    while chunk := response.read(64 * 1024):
                        if download:
                            digest.update(chunk)
                if status not in expected:
                    raise ProbeError(f"HTTP {status}")
                if download and digest.digest() != self.digest:
                    raise ProbeError("download mismatch")
                result = json.loads(payload) if decode else None
            self.record("ok")
            return result
        except HTTPError as error:
            error.close()
            self.record(f"{label}: HTTP {error.code}")
            raise ProbeError from error
        except (URLError, OSError, HTTPException, ValueError, ProbeError) as error:
            self.record(f"{label}: {type(error).__name__}")
            raise ProbeError from error
        finally:
            with self.lock:
                self.in_flight -= 1
            if self.delay and not cleanup:
                self.stop.wait(self.delay)

    def upload(self, opener):
        uploaded = self.request(opener, "POST", "/api/files?name=probe.bin", 201,
                                self.file, decode=True)
        if not isinstance(uploaded, dict) or type(uploaded.get("id")) is not int or uploaded["id"] < 1:
            self.record("upload: invalid file ID")
            raise ProbeError
        return uploaded["id"]

    def create_reference(self, opener, cookies):
        if not any(cookies):
            credentials = {"username": "p_" + uuid.uuid4().hex[:30],
                           "password": uuid.uuid4().hex}
            self.request(opener, "POST", "/api/auth/register", 201, credentials)
        return self.upload(opener)

    def cleanup(self, opener, cookies, file_id):
        if file_id is not None:
            try:
                self.request(opener, "DELETE", f"/api/files/{file_id}", 204, cleanup=True)
            except ProbeError:
                pass
        if any(cookies):
            try:
                self.request(opener, "POST", "/api/auth/logout", 204, cleanup=True)
            except ProbeError:
                pass

    def cycle(self, opener, cookies, reference=None):
        # Read an existing file even when this cycle's upload/authentication fails.
        if reference is not None:
            client, file_id = reference
            try:
                self.request(client, "GET", f"/api/files/{file_id}/download", download=True)
            except ProbeError:
                pass
        for path in PUBLIC_PATHS:
            try:
                self.request(opener, "GET", path, expected=(200, 403) if path == "/metrics" else 200)
            except ProbeError:
                pass
        # Exercise the API and static fallback routes as well.
        self.request(opener, "GET", "/api/prober-missing", expected=404)
        self.request(opener, "GET", "/prober-missing", expected=404)
        credentials = {"username": "p_" + uuid.uuid4().hex[:30],
                       "password": uuid.uuid4().hex}
        file_id = None
        try:
            self.request(opener, "POST", "/api/auth/register", 201, credentials)
            self.request(opener, "POST", "/api/auth/logout", 204)
            self.request(opener, "POST", "/api/auth/login", body=credentials)
            self.request(opener, "GET", "/api/me")
            self.request(opener, "GET", "/api/files")
            file_id = self.upload(opener)
            path = f"/api/files/{file_id}"
            self.request(opener, "GET", path)
            self.request(opener, "GET", path + "/download", download=True)
            self.request(opener, "PATCH", path, body={"name": "renamed.bin"})
        finally:
            self.cleanup(opener, cookies, file_id)

    def worker(self):
        # Each worker owns its cookies; no sessions are shared between users.
        cookies = CookieJar()
        opener = build_opener(ProxyHandler({}), HTTPCookieProcessor(cookies))
        reference_cookies = CookieJar()
        reference_client = build_opener(ProxyHandler({}), HTTPCookieProcessor(reference_cookies))
        reference_id = None
        try:
            while not self.stop.is_set():
                try:
                    if reference_id is None:
                        try:
                            reference_id = self.create_reference(reference_client, reference_cookies)
                        except ProbeError:
                            self.record("download: skipped (no reference file)")
                    reference = (reference_client, reference_id) if reference_id is not None else None
                    self.cycle(opener, cookies, reference)
                except ProbeError:
                    pass
                except Stopped:
                    break
                finally:
                    cookies.clear()
        finally:
            self.cleanup(reference_client, reference_cookies, reference_id)

    def report(self):
        with self.lock:
            counts = {"ok": 0, **self.counts, "in_flight": self.in_flight}
            self.counts.clear()
        print(json.dumps(counts, sort_keys=True), flush=True)

    def run(self):
        print(f"target={self.target} workers={self.workers} file_bytes={len(self.file)}", flush=True)
        with ThreadPoolExecutor(max_workers=self.workers) as pool:
            workers = [pool.submit(self.worker) for _ in range(self.workers)]
            while not self.stop.wait(10):
                self.report()
                for worker in workers:
                    if worker.done():
                        self.stop.set()
                        worker.result()
            for worker in workers:
                worker.result()
        self.report()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("normal", "stress"))
    args = parser.parse_args()
    try:
        prober = Prober(target_url(os.environ.get("TARGET_URL", "127.0.0.1")), args.mode)
    except ValueError as error:
        parser.error(str(error))
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, lambda *_: prober.stop.set())
    prober.run()


if __name__ == "__main__":
    main()
