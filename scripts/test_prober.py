import hashlib
from http.cookies import SimpleCookie
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from http.cookiejar import CookieJar
import json
import threading
import unittest
from concurrent.futures import ThreadPoolExecutor
from urllib.request import HTTPCookieProcessor, build_opener

from prober.main import PUBLIC_PATHS, ProbeError, Prober, target_url


class ProberTest(unittest.TestCase):
    def setUp(self):
        self.calls = []
        self.users = set()
        self.files = {}
        self.uploads = []
        self.corrupt = False
        self.fail_upload = False
        self.fail_download = False
        test = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def handle_request(self):
                body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
                cookie = SimpleCookie(self.headers.get("Cookie", ""))
                user = cookie["session"].value if "session" in cookie else None
                path = self.path.split("?")[0]
                test.calls.append((self.command, path, user))
                status, payload, session = 200, b"", None
                if self.headers.get("Origin") != test.target:
                    status = 403
                elif path.endswith("prober-missing"):
                    status = 404
                elif path == "/metrics":
                    status = 403  # Ingress intentionally blocks external scraping.
                elif path in PUBLIC_PATHS:
                    pass
                elif path in ("/api/auth/register", "/api/auth/login"):
                    user = json.loads(body)["username"]
                    if path.endswith("register"):
                        test.users.add(user)
                        status = 201
                    elif user not in test.users:
                        status = 401
                    session = "session=" + user + "; Path=/"
                elif user not in test.users:
                    status = 401
                elif path == "/api/auth/logout":
                    session = "session=; Max-Age=0; Path=/"
                    status = 204
                elif path == "/api/files" and self.command == "POST":
                    if test.fail_upload:
                        status = 503
                    else:
                        file_id = int(user[2:], 16)
                        test.files[file_id] = (user, body)
                        test.uploads.append((len(body), hashlib.sha256(body).digest()))
                        payload = json.dumps({"id": file_id}).encode()
                        status = 201
                elif path.startswith("/api/files/"):
                    file_id = int(path.split("/")[3])
                    owner, data = test.files[file_id]
                    if owner != user:
                        status = 403
                    elif path.endswith("/download"):
                        if test.fail_download:
                            status = 503
                        else:
                            payload = b"bad" if test.corrupt else data
                    elif self.command == "DELETE":
                        del test.files[file_id]
                        status = 204
                self.send_response(status)
                self.send_header("Content-Length", str(len(payload)))
                if session is not None:
                    self.send_header("Set-Cookie", session)
                self.end_headers()
                self.wfile.write(payload)

            do_GET = do_POST = do_PATCH = do_DELETE = handle_request

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.target = f"http://127.0.0.1:{self.server.server_port}"
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()

    def cycle(self, prober):
        cookies = CookieJar()
        opener = build_opener(HTTPCookieProcessor(cookies))
        prober.cycle(opener, cookies)
        self.assertEqual(list(cookies), [])

    def test_independent_users_cover_routes_and_delete_files(self):
        prober = Prober(self.target, "normal")
        self.assertEqual((prober.workers, prober.delay, len(prober.file)), (1, 1, 4096))
        prober.delay = 0
        with ThreadPoolExecutor(2) as pool:
            list(pool.map(lambda _: self.cycle(prober), range(2)))
        self.assertEqual(len(self.users), 2)
        self.assertEqual(self.files, {})
        self.assertEqual(set(prober.counts), {"ok"})
        for user in self.users:
            paths = [(method, path) for method, path, owner in self.calls if owner == user]
            self.assertIn(("GET", "/api/me"), paths)
            self.assertIn(("GET", "/api/files"), paths)
            for method in ("GET", "PATCH", "DELETE"):
                self.assertTrue(any(m == method and p.startswith("/api/files/") for m, p in paths))
            self.assertTrue(any(p.endswith("/download") for _, p in paths))
        for path in PUBLIC_PATHS:
            self.assertIn(("GET", path, None), self.calls)
        self.assertEqual(self.uploads, [(4096, prober.digest)] * 2)

    def test_stress_upload_uses_eight_mib(self):
        prober = Prober(self.target, "stress")
        self.assertEqual((prober.workers, prober.delay), (16, 0))
        self.cycle(prober)
        self.assertEqual(self.uploads, [(8 * 1024 * 1024, prober.digest)])

    def test_corrupt_download_is_failure_and_file_is_deleted(self):
        prober = Prober(self.target, "normal")
        prober.delay = 0
        self.corrupt = True
        with self.assertRaises(ProbeError):
            self.cycle(prober)
        self.assertEqual(self.files, {})
        self.assertEqual(prober.counts["GET /api/files/{id}/download: ProbeError"], 1)

    def test_reference_download_continues_when_upload_fails(self):
        prober = Prober(self.target, "normal")
        prober.delay = 0
        reference_cookies = CookieJar()
        reference_client = build_opener(HTTPCookieProcessor(reference_cookies))
        reference_id = prober.create_reference(reference_client, reference_cookies)
        self.fail_upload = True
        cookies = CookieJar()
        client = build_opener(HTTPCookieProcessor(cookies))
        try:
            for fail_download in (False, True):
                self.fail_download = fail_download
                with self.assertRaises(ProbeError):
                    prober.cycle(client, cookies, (reference_client, reference_id))
            downloads = [p for m, p, _ in self.calls if m == "GET" and p.endswith("/download")]
            self.assertEqual(downloads, [f"/api/files/{reference_id}/download"] * 2)
            self.assertEqual(prober.counts["POST /api/files: HTTP 503"], 2)
            self.assertEqual(prober.counts["GET /api/files/{id}/download: HTTP 503"], 1)
            self.assertEqual(prober.in_flight, 0)
        finally:
            prober.cleanup(reference_client, reference_cookies, reference_id)
        self.assertEqual(self.files, {})
        self.assertEqual(list(reference_cookies), [])


class TargetURLTest(unittest.TestCase):
    def test_origin(self):
        self.assertEqual(target_url("127.0.0.1"), "http://127.0.0.1")
        self.assertEqual(target_url("https://localhost:443/"), "https://localhost:443")
        for target in ("ftp://localhost", "http://user:pass@localhost", "localhost/api", "localhost:abc"):
            with self.assertRaises(ValueError):
                target_url(target)
