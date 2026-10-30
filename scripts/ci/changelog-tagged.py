#!/usr/bin/env python3
"""A released version's changelog section doesn't change after its tag.

The section is the GitHub release's text, so an entry written into it after
the tag (the first "### Added" under an empty Unreleased is an easy place to
land) never ships with that release. Each section whose v<version> tag exists
here is compared with the same section at the tag. `bun run check` runs this;
CI's shallow checkout has no tags, so it checks nothing there.
"""
import re
import subprocess
import sys


def sections(text):
    out, name, buf = {}, None, []
    for line in text.splitlines():
        m = re.match(r"^## \[([^\]]+)\]", line)
        if m or re.match(r"^\[[^\]]+\]: ", line):
            if name:
                out[name] = "\n".join(buf).strip()
            name, buf = (m.group(1), []) if m else (None, [])
        elif name:
            buf.append(line)
    if name:
        out[name] = "\n".join(buf).strip()
    return out


tags = set(subprocess.run(["git", "tag", "-l", "v*"], check=True, capture_output=True, text=True).stdout.split())
now = sections(open("CHANGELOG.md", encoding="utf-8").read())
changed = []
for version, body in now.items():
    tag = "v" + version
    if tag not in tags:
        continue
    then = subprocess.run(["git", "show", f"{tag}:CHANGELOG.md"], capture_output=True, text=True)
    if then.returncode != 0:
        continue
    if sections(then.stdout).get(version) != body:
        changed.append(version)
for version in changed:
    print(f"CHANGELOG.md: the {version} section changed after v{version} was tagged; new entries go under Unreleased")
sys.exit(1 if changed else 0)
