import hashlib
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import MagicMock, patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("publisher", ROOT / "scripts/release/publish-installer.py")
publisher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(publisher)


class PublishInstallerTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.source = Path(self.temp.name) / "install.sh"
        self.content = b'#!/bin/sh\nprintf "installer test\\n"\n'
        self.source.write_bytes(self.content)
        self.objects = {}
        self.uploaded = []
        self.corrupt_origin = False
        self.corrupt_cdn = False
        self.cache = "no-store"
        self.enterContext(patch.dict(os.environ, {
            "R2_ENDPOINT": "https://r2.example", "R2_BUCKET": "test",
            "AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test",
        }, clear=True))
        self.aws = self.enterContext(patch.object(publisher, "aws", side_effect=self.storage))
        self.open = self.enterContext(patch.object(publisher, "urlopen", side_effect=self.public))
        self.enterContext(patch.object(publisher.time, "sleep"))

    def storage(self, operation, *args):
        key = args[args.index("--key") + 1]
        if operation == "put-object":
            body = Path(args[args.index("--body") + 1]).read_bytes()
            self.objects[key] = (body, {
                "ContentType": args[args.index("--content-type") + 1],
                "CacheControl": args[args.index("--cache-control") + 1],
            })
            self.uploaded.append(key)
            return {}
        body, metadata = self.objects[key]
        Path(args[args.index("--key") + 2]).write_bytes(b"wrong" if self.corrupt_origin else body)
        return metadata

    def public(self, url, timeout):
        key = url.removeprefix(publisher.CDN + "/")
        self.assertNotIn("?", key)
        response = MagicMock()
        response.__enter__.return_value.read.return_value = b"stale" if self.corrupt_cdn else self.objects[key][0]
        response.__enter__.return_value.headers = {"Cache-Control": self.cache}
        return response

    def test_verified_snapshot_precedes_latest_and_retry_is_idempotent(self):
        digest = hashlib.sha256(self.content).hexdigest()
        expected_keys = [f"installers/sha256/{digest}/install.sh", publisher.LATEST_KEY]
        publisher.publish(self.source)
        self.assertEqual(self.uploaded, expected_keys)
        self.assertEqual(self.objects[publisher.LATEST_KEY][0], self.content)
        publisher.publish(self.source)
        self.assertEqual(self.uploaded, expected_keys * 2)
        self.assertEqual(len(self.objects), 2)

    def test_invalid_script_does_not_upload(self):
        self.source.write_bytes(b"#!/bin/sh\nif then\n")
        with self.assertRaises(subprocess.CalledProcessError):
            publisher.publish(self.source)
        self.aws.assert_not_called()

    def test_empty_source_does_not_upload(self):
        self.source.write_bytes(b"")
        with self.assertRaises(ValueError):
            publisher.publish(self.source)
        self.aws.assert_not_called()

    def test_missing_credentials_does_not_upload(self):
        with patch.dict(os.environ, {"R2_BUCKET": ""}):
            with self.assertRaises(ValueError):
                publisher.publish(self.source)
        self.aws.assert_not_called()

    def test_origin_mismatch_does_not_advance_latest(self):
        self.corrupt_origin = True
        with self.assertRaisesRegex(RuntimeError, "R2 content"):
            publisher.publish(self.source)
        self.assertNotIn(publisher.LATEST_KEY, self.uploaded)
        self.open.assert_not_called()

    def test_stale_snapshot_does_not_advance_latest(self):
        self.corrupt_cdn = True
        with self.assertRaisesRegex(RuntimeError, "CDN content"):
            publisher.publish(self.source)
        self.assertNotIn(publisher.LATEST_KEY, self.uploaded)
        self.assertEqual(self.open.call_count, 3)

    def test_latest_cache_misconfiguration_fails(self):
        self.cache = "public, max-age=3600"
        with self.assertRaisesRegex(RuntimeError, "no-store"):
            publisher.publish(self.source)

    def test_upload_error_stops_before_verification(self):
        self.aws.side_effect = subprocess.CalledProcessError(1, "aws")
        with self.assertRaises(subprocess.CalledProcessError):
            publisher.publish(self.source)
        self.open.assert_not_called()
