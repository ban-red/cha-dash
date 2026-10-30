#!/usr/bin/env python3
"""Go tests don't name real home directories.

A test that reads "/root/…" or "/home/…" passes on a laptop where the path
doesn't exist, and fails on a CI runner where it exists but can't be read
(a .env next to a compose file, say). Use t.TempDir() instead. A line that
needs the literal (a path inside a container, never opened) ends with
`// test-paths: ok`. CI's Go job and `bun run check` both run this.
"""
import os
import re
import subprocess
import sys

ANNOTATE = os.environ.get("GITHUB_ACTIONS") == "true"
HOME = re.compile(r'"(/root|/home)/')  # a path under one: "/home" alone is a URL path in places

files = subprocess.run(["git", "ls-files", "-z", "*_test.go"], check=True, capture_output=True, text=True).stdout.split("\0")
found = 0
for path in filter(None, files):
    with open(path, encoding="utf-8") as f:
        for n, line in enumerate(f, 1):
            if HOME.search(line) and "test-paths: ok" not in line:
                found += 1
                msg = "a real home directory in a test: use t.TempDir()"
                print(f"::error file={path},line={n}::{msg}" if ANNOTATE else f"{path}:{n}: {msg}")
sys.exit(1 if found else 0)
