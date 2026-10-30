#!/usr/bin/env python3
"""The Dockerfile copies every package the hub and the node import.

Each binary's build stage must COPY every top-level directory its
`go list -deps` names: the hub's stage (hub) and the node's (node-build),
checked separately. CI's Go job and `bun run check` both run this.
"""
import os
import re
import subprocess
import sys

ANNOTATE = os.environ.get("GITHUB_ACTIONS") == "true"


def error(msg):
    print(f"::error::{msg}" if ANNOTATE else msg)


lines = open("Dockerfile", encoding="utf-8").read().splitlines()


def stage(name):
    out, inside = [], False
    for line in lines:
        if line.startswith("FROM "):
            inside = line.rstrip().endswith(" AS " + name)
        elif inside:
            out.append(line.strip())
    return out


failed = False
for pkg, name in (("./cmd/chadash", "hub"), ("./cmd/chadash-node", "node-build")):
    deps = subprocess.run(["go", "list", "-deps", pkg], check=True, capture_output=True, text=True).stdout.split()
    dirs = sorted({m.group(1) for d in deps if (m := re.match(r"dash\.cha\.sh/([^/]+)", d))})
    body = stage(name)
    if not body:
        error(f"No '{name}' stage in the Dockerfile")
        sys.exit(1)
    missing = [d for d in dirs if f"COPY {d} {d}" not in body]
    if missing:
        error(f"Add COPY lines to the Dockerfile's {name} stage for: {' '.join(missing)}")
        failed = True
sys.exit(1 if failed else 0)
