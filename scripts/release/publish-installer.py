#!/usr/bin/env python3
"""Publish only the shell installer, then verify the exact public CDN URL."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
from urllib.request import urlopen

CDN = "https://cdn.eigenflux.ai"
LATEST_KEY = "installers/latest/install.sh"
CONTENT_TYPE = "text/plain; charset=utf-8"
ATTEMPTS = 3


def aws(*args):
    result = subprocess.run(
        ["aws", "--endpoint-url", os.environ["R2_ENDPOINT"], "--region", "auto",
         "--cli-connect-timeout", "10", "--cli-read-timeout", "20",
         "s3api", *args, "--bucket", os.environ["R2_BUCKET"], "--output", "json"],
        check=True, capture_output=True, text=True, timeout=45,
    )
    return json.loads(result.stdout or "{}")


def verify(key, expected, cache_control, directory):
    for attempt in range(ATTEMPTS):
        try:
            downloaded = directory / "downloaded.sh"
            metadata = aws("get-object", "--key", key, str(downloaded))
            if (downloaded.read_bytes() != expected
                    or metadata.get("ContentType") != CONTENT_TYPE
                    or metadata.get("CacheControl") != cache_control):
                raise RuntimeError(f"R2 content or metadata mismatch: {key}")
            # No cache-busting query: verify the URL that clients actually use.
            with urlopen(f"{CDN}/{key}", timeout=15) as response:
                if response.read(len(expected) + 1) != expected:
                    raise RuntimeError(f"CDN content mismatch: {key}")
                if key == LATEST_KEY:
                    directives = {s.strip().lower() for s in response.headers.get("Cache-Control", "").split(",")}
                    if "no-store" not in directives:
                        raise RuntimeError("CDN latest installer must return Cache-Control: no-store")
            return
        except (OSError, RuntimeError, subprocess.SubprocessError) as error:
            if attempt == ATTEMPTS - 1:
                raise RuntimeError(f"Verification failed: {key}: {error}") from error
            time.sleep(5)


def publish(source):
    for name in ("R2_ENDPOINT", "R2_BUCKET", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"):
        if not os.environ.get(name):
            raise ValueError(f"{name} is required")
    expected = source.read_bytes()
    if not expected.startswith(b"#!/bin/sh\n"):
        raise ValueError("Expected a nonempty POSIX shell installer")
    subprocess.run(["sh", "-n", str(source)], check=True, timeout=15)
    digest = hashlib.sha256(expected).hexdigest()
    with tempfile.TemporaryDirectory(prefix="eigenflux-installer-") as temp:
        directory = Path(temp)
        snapshot = directory / "install.sh"
        snapshot.write_bytes(expected)
        # The content-addressed copy supports audit and recovery. Verify it before
        # advancing latest; retries of identical source write identical bytes.
        for key, cache in (
            (f"installers/sha256/{digest}/install.sh", "public, max-age=31536000, immutable"),
            (LATEST_KEY, "no-store"),
        ):
            aws("put-object", "--key", key, "--body", str(snapshot),
                "--content-type", CONTENT_TYPE, "--cache-control", cache)
            verify(key, expected, cache, directory)
    message = f"Published {CDN}/{LATEST_KEY}\nSHA-256: {digest}\n"
    print(message)
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as summary:
            summary.write(message)


if __name__ == "__main__":
    try:
        publish(Path(__file__).resolve().parents[2] / "static/install.sh")
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"Installer publish failed: {error}", file=sys.stderr)
        sys.exit(1)
