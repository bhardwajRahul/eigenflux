"""Offline checks for the temporary split-Skill branch installer overlay."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
BRANCH_BASE = (
    "https://raw.githubusercontent.com/phronesis-io/eigenflux/"
    "codex/split-install-onboarding-skills/skills/"
)
DOCS = (
    "ef-onboarding/SKILL.md",
    "ef-onboarding/references/consent.md",
    "ef-onboarding/references/prefill.md",
    "ef-onboarding/references/recurring-trigger.md",
    "ef-onboarding/references/console-handoff.md",
    "ef-profile/SKILL.md",
    "ef-profile/references/config.md",
    "ef-broadcast/SKILL.md",
    "ef-communication/SKILL.md",
)


class SplitSkillOverlayTest(unittest.TestCase):
    def run_overlay(self, failed_doc=""):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "bin"
            skills = root / "skills"
            binary.mkdir()
            (skills / "ef-profile/references").mkdir(parents=True)
            (skills / "ef-profile/SKILL.md").write_text("released profile")
            (skills / "ef-profile/references/config.md").write_text("released config")
            legacy = skills / "ef-profile/references/onboarding-v2.md"
            legacy.write_text("released onboarding")

            cli = binary / "eigenflux"
            cli.write_text('#!/bin/sh\nprintf "%s\\n" "$TEST_ROOT/skills"\n')
            curl = binary / "curl"
            curl.write_text(
                """#!/bin/sh
case "$2" in
  "${BRANCH_BASE}"*) ;;
  *) exit 8 ;;
esac
case "$2" in
  *"${FAILED_DOC}") [ -z "$FAILED_DOC" ] || exit 22 ;;
esac
printf 'branch:%s' "$2" > "$4"
"""
            )
            cli.chmod(0o755)
            curl.chmod(0o755)
            env = dict(
                os.environ,
                PATH=str(binary) + ":" + os.environ["PATH"],
                TEST_ROOT=str(root),
                BRANCH_BASE=BRANCH_BASE,
                FAILED_DOC=failed_doc,
                EIGENFLUX_INSTALL_DIR=str(binary),
                EIGENFLUX_INSTALLER_TEST_MODE="1",
            )
            result = subprocess.run(
                [
                    "sh",
                    "-c",
                    '. "$1"; install_split_skill_test_docs',
                    "test",
                    str(ROOT / "static/install.sh"),
                    "--host",
                    "codex",
                ],
                env=env,
                capture_output=True,
                text=True,
            )

            self.assertEqual(result.returncode == 0, not failed_doc, result.stderr)
            if failed_doc:
                self.assertEqual((skills / "ef-profile/SKILL.md").read_text(), "released profile")
                self.assertEqual((skills / "ef-profile/references/config.md").read_text(), "released config")
                self.assertEqual(legacy.read_text(), "released onboarding")
                self.assertFalse((skills / "ef-onboarding").exists())
                return

            for relative in DOCS:
                self.assertEqual(
                    (skills / relative).read_text(), BRANCH_BASE.join(("branch:", relative))
                )
            self.assertFalse(legacy.exists())

    def test_installs_all_split_skill_documents(self):
        self.run_overlay()

    def test_failed_download_leaves_released_documents_unchanged(self):
        self.run_overlay("ef-onboarding/references/prefill.md")


if __name__ == "__main__":
    unittest.main()
