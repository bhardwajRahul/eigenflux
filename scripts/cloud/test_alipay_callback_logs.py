#!/usr/bin/env python3
"""Exercise the shared callback log filter with a real, local Caddy binary."""

import http.client
import http.server
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import threading
import time
import unittest
from urllib.parse import urlencode


CALLBACK = "/api/v2/console/alipay/authorization/callback"
REPO = Path(__file__).resolve().parents[2]


class Upstream(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        case = self.headers.get("X-QA-Case", "ready")
        self.server.request_paths[case] = self.path
        if self.headers.get("X-QA-Fail") == "1":
            self.close_connection = True
            self.connection.shutdown(socket.SHUT_RDWR)
            self.connection.close()
            return
        body = self.path.encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_args):
        pass


class AlipayCallbackLogsTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        binary = os.environ.get("CADDY_BIN")
        if not binary or not Path(binary).is_file():
            raise RuntimeError("Set CADDY_BIN to a local Caddy binary before running this test")
        cls.temp = tempfile.TemporaryDirectory(prefix="eigenflux-callback-logs-")
        cls.addClassCleanup(cls.temp.cleanup)
        cls.directory = Path(cls.temp.name)
        cls.log_paths = {
            "access-file": cls.directory / "access.jsonl",
            "access-journal": cls.directory / "stderr.jsonl",
            "default": cls.directory / "errors.jsonl",
        }
        cls.upstream = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
        cls.upstream.request_paths = {}
        cls.upstream_thread = threading.Thread(target=cls.upstream.serve_forever, daemon=True)
        cls.upstream_thread.start()
        cls.addClassCleanup(cls.stop_upstream)
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            cls.port = listener.getsockname()[1]
        config = cls.directory / "Caddyfile"
        snippet = REPO / "cloud/caddy/alipay_callback_log_filter.caddy"
        config.write_text(
            f"""import {json.dumps(str(snippet))}
{{
    admin off
    auto_https off
    persist_config off
    log default {{
        output file {json.dumps(str(cls.log_paths['default']))}
        import alipay_callback_log_filter
        exclude http.log.access
    }}
    log access-journal {{
        output stderr
        import alipay_callback_log_filter
        include http.log.access.access-file
    }}
}}
http://127.0.0.1:{cls.port} {{
    log access-file {{
        output file {json.dumps(str(cls.log_paths['access-file']))}
        import alipay_callback_log_filter
    }}
    reverse_proxy 127.0.0.1:{cls.upstream.server_port}
}}
""",
            encoding="utf-8",
        )
        cls.stderr = cls.log_paths["access-journal"].open("wb")
        cls.addClassCleanup(cls.stderr.close)
        environment = dict(os.environ)
        environment.update(
            XDG_CONFIG_HOME=str(cls.directory / "config"),
            XDG_DATA_HOME=str(cls.directory / "data"),
        )
        cls.process = subprocess.Popen(
            [str(Path(binary).resolve()), "run", "--config", str(config), "--adapter", "caddyfile"],
            cwd=cls.directory,
            env=environment,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=cls.stderr,
        )
        cls.addClassCleanup(cls.stop_caddy)
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if cls.process.poll() is not None:
                raise RuntimeError(cls.log_paths["access-journal"].read_text(encoding="utf-8"))
            try:
                status, _ = cls.request("/_qa_ready", "ready")
                if status == 200:
                    return
            except (OSError, http.client.HTTPException):
                pass
            time.sleep(0.05)
        raise RuntimeError("Local Caddy did not become ready within 10 seconds")

    @classmethod
    def stop_caddy(cls):
        if cls.process.poll() is None:
            cls.process.terminate()
            try:
                cls.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                cls.process.kill()
                cls.process.wait(timeout=5)

    @classmethod
    def stop_upstream(cls):
        cls.upstream.shutdown()
        cls.upstream.server_close()
        cls.upstream_thread.join(timeout=5)

    @classmethod
    def request(cls, target, case, fail=False):
        connection = http.client.HTTPConnection("127.0.0.1", cls.port, timeout=2)
        try:
            headers = {"X-QA-Case": case}
            if fail:
                headers["X-QA-Fail"] = "1"
            connection.request("GET", target, headers=headers)
            response = connection.getresponse()
            return response.status, response.read().decode("utf-8")
        finally:
            connection.close()

    def assert_logged(self, sink, case, uri, status):
        deadline = time.monotonic() + 3
        path = self.log_paths[sink]
        while time.monotonic() < deadline:
            if path.exists():
                for line in path.read_text(encoding="utf-8").splitlines():
                    try:
                        entry = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    headers = entry.get("request", {}).get("headers", {})
                    if not any(name.lower() == "x-qa-case" and case in values for name, values in headers.items()):
                        continue
                    self.assertEqual(entry["request"]["uri"], uri, sink)
                    self.assertEqual(entry["status"], status, sink)
                    if sink == "default":
                        self.assertEqual(entry["logger"], "http.log.error.access-file")
                    else:
                        self.assertEqual(entry["logger"], "http.log.access.access-file")
                    return entry
            time.sleep(0.02)
        self.fail(f"No {sink} entry found for fixture {case}")

    def assert_absent_from_all_logs(self, values):
        for sink, path in self.log_paths.items():
            if path.exists():
                contents = path.read_text(encoding="utf-8")
                for value in values:
                    self.assertNotIn(value, contents, sink)

    def test_callback_query_is_redacted_from_both_access_sinks_only(self):
        query = urlencode({"state": "fictional-state-success", "auth_code": "fictional-code-success", "extra": "fictional-extra"})
        target = f"{CALLBACK}?{query}"
        status, body = self.request(target, "callback-success")
        self.assertEqual(status, 200)
        self.assertEqual(body, target)
        self.assertEqual(self.upstream.request_paths["callback-success"], target)
        for sink in ("access-file", "access-journal"):
            self.assert_logged(sink, "callback-success", CALLBACK, 200)
        self.assert_absent_from_all_logs(("fictional-state-success", "fictional-code-success", "fictional-extra"))

    def test_non_callback_and_queryless_callback_are_unchanged(self):
        targets = [
            CALLBACK,
            CALLBACK + "-extra?state=fixture-other&auth_code=fixture-other-code",
            "/api/v2/console/alipay/authorization/result?state=fixture-result",
            "/api/v1/website/stats?source=fixture-stats",
        ]
        for index, target in enumerate(targets):
            with self.subTest(target=target):
                case = f"unchanged-{index}"
                status, body = self.request(target, case)
                self.assertEqual((status, body), (200, target))
                self.assertEqual(self.upstream.request_paths[case], target)
                for sink in ("access-file", "access-journal"):
                    self.assert_logged(sink, case, target, 200)

    def test_upstream_failure_redacts_default_and_both_access_sinks(self):
        target = CALLBACK + "?state=fictional-state-error&auth_code=fictional-code-error&extra=fictional-error-extra"
        status, _ = self.request(target, "callback-upstream-error", fail=True)
        self.assertEqual(status, 502)
        self.assertEqual(self.upstream.request_paths["callback-upstream-error"], target)
        for sink in ("access-file", "access-journal", "default"):
            self.assert_logged(sink, "callback-upstream-error", CALLBACK, 502)
        self.assert_absent_from_all_logs(("fictional-state-error", "fictional-code-error", "fictional-error-extra"))


if __name__ == "__main__":
    unittest.main(verbosity=2)
