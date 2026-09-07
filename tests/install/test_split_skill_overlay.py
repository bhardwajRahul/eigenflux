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
    def run_test_home_setup(self, homedir="", inherited_home=""):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fake_home = root / "user-home"
            workspace = root / "test-project"
            fake_home.mkdir()
            workspace.mkdir()
            env = dict(
                os.environ,
                HOME=str(fake_home),
                EIGENFLUX_HOME=inherited_home,
                EIGENFLUX_INSTALLER_TEST_MODE="1",
            )
            command = (
                '. "$1"; prepare_split_test_home; '
                'printf "resolved=%s\\nflag=%s\\n" "$EIGENFLUX_HOME" "$HOMEDIR_FLAG"'
            )
            args = [
                "sh",
                "-c",
                command,
                "test",
                str(ROOT / "static/install.sh"),
            ]
            if homedir:
                args.extend(("--homedir", homedir))
            result = subprocess.run(
                args, env=env, cwd=workspace, capture_output=True, text=True
            )
            self.assertEqual(result.returncode, 0, result.stderr)

            pointer = workspace / ".eigenflux-tests/current-home"
            self.assertTrue(pointer.is_file())
            resolved = pointer.read_text().strip()
            self.assertIn(f"resolved={resolved}", result.stdout)
            self.assertIn(f"flag={resolved}", result.stdout)
            self.assertTrue(Path(resolved).is_dir())
            return workspace, resolved

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

    def test_creates_fresh_test_home_instead_of_reusing_inherited_home(self):
        inherited = "/existing/production/.eigenflux"
        workspace, resolved = self.run_test_home_setup(inherited_home=inherited)
        self.assertNotEqual(resolved, inherited)
        self.assertTrue(
            str(Path(resolved).resolve()).startswith(
                str((workspace / ".eigenflux-tests/split-onboarding-").resolve())
            )
        )
        self.assertTrue(resolved.endswith("/.eigenflux"))

    def test_explicit_homedir_is_recorded_and_reused(self):
        with tempfile.TemporaryDirectory() as directory:
            selected = str(Path(directory) / "chosen/.eigenflux")
            _, resolved = self.run_test_home_setup(homedir=selected)
            self.assertEqual(resolved, selected)


if __name__ == "__main__":
    unittest.main()
