import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("release_state", ROOT / "cli/scripts/skills-release-state.py")
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class SequenceTests(unittest.TestCase):
    def test_conflicting_legacy_eight_recovers_at_nine(self):
        records = [{"sequence": 8, "revision": "old"}, {"sequence": 8, "revision": "new"}]
        self.assertEqual(release.next_sequence(records), 9)
        release.require_transition({"sequence": 9}, records, 9)
        for candidate in (7, 8, 10):
            with self.assertRaises(RuntimeError):
                release.require_transition({"sequence": candidate}, records, 9)

    def test_partial_release_consumes_reserved_number(self):
        records = [{"sequence": 8}, {"sequence": 8}, {"sequence": 9}]
        self.assertEqual(release.next_sequence(records), 10)
        with self.assertRaises(RuntimeError):
            release.require_transition({"sequence": 9}, records, 9)

    def test_sequence_overflow_fails_closed(self):
        with self.assertRaises(RuntimeError):
            release.next_sequence([{"sequence": 2**64 - 1}])

    def test_archive_uses_numeric_max_and_only_manifest_keys(self):
        objects = {"Contents": [{"Key": key} for key in (
            "skills/releases/9/manifest.json", "skills/releases/10/manifest.json",
            "skills/releases/999/skills.tar.gz", "skills/releases/bogus/manifest.json") ]}
        with patch.object(release, "aws", return_value=objects):
            self.assertEqual(release.archive_head(), 10)

    def test_snapshot_fails_on_invalid_signature_or_network_error(self):
        with tempfile.TemporaryDirectory() as temp:
            for failure in (RuntimeError("bad signature"), subprocess.CalledProcessError(1, "aws")):
                with patch.object(release, "aws"), patch.object(release, "verified", side_effect=failure):
                    with self.assertRaises(type(failure)):
                        release.snapshot(Path(temp), Path("checker"))

    def test_archive_sequence_must_match_signed_record(self):
        with tempfile.TemporaryDirectory() as temp:
            with patch.object(release, "aws"), patch.object(release, "archive_head", return_value=10), patch.object(release, "verified", return_value={"sequence": 8}):
                with self.assertRaisesRegex(RuntimeError, "disagree"):
                    release.snapshot(Path(temp), Path("checker"))

    def test_reservation_collision_stops_before_archive_upload(self):
        with tempfile.TemporaryDirectory() as temp:
            build = Path(temp)
            state = build / "release-state"
            state.mkdir()
            records = [{"sequence": 8}]
            (state / "plan.json").write_text(json.dumps({"sequence": 9, "records": records}))
            (build / "skills.tar.gz").write_bytes(b"archive")
            digest = release.hashlib.sha256(b"archive").hexdigest()
            (build / "skills.tar.gz.sha256").write_text(digest)
            candidate = {"sequence": 9, "tar_sha256": digest}
            with patch.object(release, "verified", return_value=candidate), patch.object(release, "snapshot", return_value=records), patch.object(release, "aws", side_effect=subprocess.CalledProcessError(1, "aws")) as aws:
                with self.assertRaises(subprocess.CalledProcessError):
                    release.reserve(state, build / "checker", build)
                self.assertEqual(aws.call_count, 1)
                args = aws.call_args.args
                self.assertIn("skills/releases/9/manifest.json", args)
                self.assertEqual(args[-2:], ("--if-none-match", "*"))

    def test_changed_origin_prevents_reservation(self):
        with tempfile.TemporaryDirectory() as temp:
            state = Path(temp)
            (state / "plan.json").write_text(json.dumps({"sequence": 9, "records": [{"sequence": 8, "revision": "old"}]}))
            with patch.object(release, "verified", return_value={"sequence": 9}), patch.object(release, "snapshot", return_value=[{"sequence": 8, "revision": "changed"}]), patch.object(release, "aws") as aws:
                with self.assertRaisesRegex(RuntimeError, "changed"):
                    release.reserve(state, state / "checker", state)
                aws.assert_not_called()

    def test_successful_reservation_archives_manifest_before_payload(self):
        with tempfile.TemporaryDirectory() as temp:
            build = Path(temp)
            records = [{"sequence": 8}]
            (build / "plan.json").write_text(json.dumps({"sequence": 9, "records": records}))
            (build / "skills.tar.gz").write_bytes(b"archive")
            digest = release.hashlib.sha256(b"archive").hexdigest()
            (build / "skills.tar.gz.sha256").write_text(digest)
            with patch.object(release, "verified", return_value={"sequence": 9, "tar_sha256": digest}), patch.object(release, "snapshot", return_value=records), patch.object(release, "put_new") as put:
                release.reserve(build, build / "checker", build)
                self.assertEqual([call.args[1] for call in put.call_args_list], [
                    "skills/releases/9/manifest.json", "skills/releases/9/skills.tar.gz",
                    "skills/releases/9/skills.tar.gz.sha256"])

    def test_live_verification_rejects_mismatched_alias(self):
        with tempfile.TemporaryDirectory() as temp:
            state = Path(temp)
            with patch.object(release, "aws"), patch.object(release, "verified", side_effect=[{"sequence": 9}, {"sequence": 9}, {"sequence": 8}]):
                with self.assertRaisesRegex(RuntimeError, "differs"):
                    release.verify_live(state, state / "checker", state)

    def test_manual_release_dispatches_main_without_loading_credentials(self):
        with tempfile.TemporaryDirectory() as temp:
            fake_gh = Path(temp) / "gh"
            fake_gh.write_text('#!/bin/sh\nprintf "%s\\n" "$@"\n')
            fake_gh.chmod(0o700)
            result = subprocess.run(["/bin/bash", str(ROOT / "cli/scripts/release-skills.sh")], env={"PATH": temp + ":/usr/bin:/bin"}, check=True, capture_output=True, text=True)
            self.assertEqual(result.stdout.splitlines(), ["workflow", "run", "release-skills.yml", "--repo", "phronesis-io/eigenflux", "--ref", "main"])

    def test_other_workflow_cannot_publish(self):
        result = subprocess.run(["/bin/bash", str(ROOT / "cli/scripts/release-skills.sh")], env={"PATH": "/usr/bin:/bin", "GITHUB_ACTIONS": "true", "GITHUB_WORKFLOW_REF": "other"}, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("requires the Release Skills workflow", result.stderr)


if __name__ == "__main__":
    unittest.main()
