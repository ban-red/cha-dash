#!/bin/sh
# Prints the CHANGELOG.md section for one version: the body of its GitHub
# release. .github/workflows/release.yml runs it; run it yourself to preview.
#
#   scripts/release-notes.sh 0.1.0-beta.1     (a leading v is fine too)
#
# The section is everything under "## [<version>]" up to the next "## [" or
# the link definitions at the bottom. Exits 1 if it's missing or empty.
set -eu

version=${1:?usage: scripts/release-notes.sh <version, e.g. 0.1.0-beta.1> [changelog]}
version=${version#v}
changelog=${2:-CHANGELOG.md}

notes=$(awk -v v="$version" '
	/^## \[/ {
		if (found) exit
		if (index($0, "## [" v "]") == 1) { found = 1; next }
	}
	found && /^\[[^]]+\]: / { exit }
	found { print }
' "$changelog")

if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ]; then
	echo "release-notes: $changelog has no \"## [$version]\" section, or it's empty" >&2
	exit 1
fi
# Without the blank lines at either end.
printf '%s\n' "$notes" | sed '/./,$!d'
