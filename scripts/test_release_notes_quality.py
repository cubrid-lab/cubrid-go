"""Read-only release-note quality regression fixtures (standard library only)."""
import unittest
from release_notes_quality import violations

REPO = "cubrid-lab/example"
GOOD = """## v1.2.0 (2026-11-17)

This update makes connection failures easier to diagnose without changing the public API.

### Bug Fixes

- Report connection errors with a clearer message ([#12](https://github.com/cubrid-lab/example/pull/12)) by @reviewed-human

**Full Changelog:** [v1.1.0...v1.2.0](https://github.com/cubrid-lab/example/compare/v1.1.0...v1.2.0)
"""


class ReleaseNotesQualityTests(unittest.TestCase):
    def check(self, body=GOOD, version="v1.2.0"):
        return violations(body, version, REPO)

    def test_valid_short_patch(self):
        self.assertEqual([], self.check())

    def test_missing_user_summary(self):
        body = GOOD.replace(
            "This update makes connection failures easier to diagnose without changing the public API.\n", ""
        )
        self.assertTrue(any("summary" in x for x in self.check(body)))

    def test_duplicate_change_entry(self):
        line = next(line for line in GOOD.splitlines() if line.startswith("- "))
        body = GOOD.replace(line, line + "\n" + line)
        self.assertTrue(any("duplicate bullet" in x for x in self.check(body)))

    def test_generated_bullet_is_not_curated(self):
        body = GOOD.replace("- Report connection errors", "* fix: report connection errors")
        self.assertTrue(any("uncurated" in x for x in self.check(body)))

    def test_wrong_version_link(self):
        body = GOOD.replace("compare/v1.1.0...v1.2.0", "compare/v1.1.0...v1.1.1")
        self.assertTrue(any("tag" in x for x in self.check(body)))

    def test_first_release_commit_link(self):
        body = GOOD.replace("v1.1.0...v1.2.0](https://github.com/cubrid-lab/example/compare/v1.1.0...v1.2.0)",
                            "v1.2.0 commits](https://github.com/cubrid-lab/example/commits/v1.2.0)")
        self.assertEqual([], self.check(body))

    def test_wrong_heading(self):
        self.assertTrue(any("heading" in x for x in self.check(GOOD, "v1.3.0")))


if __name__ == "__main__":
    unittest.main()
