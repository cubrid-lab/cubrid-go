#!/usr/bin/env python3
"""Read-only, version-scoped checks for human-readable GitHub Release notes.

This catches mechanical errors; reviewers must still verify technical truth,
security and migration caveats, contributor identities, and tagged provenance.
It neither fetches nor edits GitHub Releases.
"""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

TAG = re.compile(r"^## (v\d+\.\d+\.\d+) \(\d{4}-\d{2}-\d{2}\)$")
SECTION = re.compile(r"^### ([^\n]+)$")
LINK = re.compile(r"https://github\.com/[^)\s]+")
ALLOWED = {
    "Upgrade notes", "Compatibility", "Added", "Changed", "Deprecated",
    "Removed", "Fixed", "Security", "Performance", "Features", "Bug Fixes",
    "Documentation", "CI", "Tests",
}
RAW_GENERATED = re.compile(r"^(?:\*\s+|-\s+\*\*(?:feat|fix|chore|ci|docs|test):)", re.I)
# User-facing changes need credit on their own entry; compatibility warnings
# may instead describe constraints without claiming that someone implemented them.
ATTRIBUTED_SECTIONS = {
    "Added", "Changed", "Deprecated", "Removed", "Fixed", "Security",
    "Performance", "Features", "Bug Fixes", "Documentation", "CI", "Tests",
}


def violations(text: str, version: str, repository: str) -> list[str]:
    """Report objective regressions without rewriting the historical text."""
    errors: list[str] = []
    lines = text.strip().splitlines()
    if not lines or not (header := TAG.fullmatch(lines[0])):
        return ["expected '## vX.Y.Z (YYYY-MM-DD)' as the first line"]
    if header.group(1) != version:
        errors.append(f"heading {header.group(1)} does not match file {version}")
    sections = [(i, SECTION.fullmatch(line).group(1))
                for i, line in enumerate(lines) if SECTION.fullmatch(line)]
    if not sections:
        errors.append("missing categorized ### changes")
        return errors
    summary = " ".join(line.strip() for line in lines[1:sections[0][0]] if line.strip())
    if not summary or summary.startswith(("### ", "- ", "* ")):
        errors.append("missing reader-facing summary before the first ### heading")
    if len(summary) > 900:
        errors.append("summary exceeds 900 characters; move details into relevant sections")

    names = [title for _, title in sections]
    if len(names) != len(set(names)):
        errors.append("duplicate section heading")
    for title in names:
        if title not in ALLOWED:
            errors.append(f"nonstandard section: {title}")

    full = [line for line in lines if line.startswith("**Full Changelog:**")]
    if len(full) != 1 or lines[-1] != full[0]:
        errors.append("expected exactly one Full Changelog link at the end")
    else:
        links = LINK.findall(full[0])
        if len(links) != 1 or not links[0].startswith(f"https://github.com/{repository}/"):
            errors.append("Full Changelog must point to this repository")
        elif not (
            links[0].endswith(f"/{version}")
            or links[0].endswith(f"...{version}")
        ):
            errors.append("Full Changelog must end at this version's tag")

    seen: set[str] = set()
    bullets = 0
    for idx, (start, title) in enumerate(sections):
        end = sections[idx + 1][0] if idx + 1 < len(sections) else len(lines)
        entries = [line for line in lines[start + 1:end] if line.startswith(("- ", "* "))]
        if not entries:
            errors.append(f"empty section: {title}")
        for line in entries:
            bullets += 1
            if RAW_GENERATED.search(line):
                errors.append(f"uncurated release-please bullet under {title}")
            if len(line.strip()) < 12:
                errors.append(f"non-informative bullet under {title}")
            if title in ATTRIBUTED_SECTIONS and not re.search(
                r"\bby @[A-Za-z0-9][A-Za-z0-9-]*\b", line
            ):
                errors.append(f"missing contributor credit under {title}")
            key = re.sub(r"\s+", " ", line.strip().casefold())
            if key in seen:
                errors.append(f"duplicate bullet under {title}")
            seen.add(key)
    if not bullets:
        errors.append("no changes described")
    if " by @" not in text:
        errors.append("missing traceable contributor byline; verify author evidence")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("files", nargs="*", help="Versioned docs/releases/vX.Y.Z.md files")
    args = parser.parse_args()
    repository = "cubrid-lab/cubrid-go"
    files = [Path(p) for p in args.files] or sorted(Path("docs/releases").glob("v*.md"))
    if not files:
        print("ERROR: no versioned release notes found", file=sys.stderr)
        return 1
    failures = 0
    for path in files:
        for problem in violations(path.read_text(encoding="utf-8"), path.stem, repository):
            print(f"{path}: {problem}", file=sys.stderr)
            failures += 1
    if failures:
        print(f"Release note quality: {failures} problem(s)", file=sys.stderr)
        return 1
    print(f"Release note quality: {len(files)} version(s) passed (read-only)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
