#!/bin/sh
# Cha Dash installer: the hub and chadash-node with Docker Compose, pinned to
# one release.
#
#   curl -fsSL https://github.com/ban-red/cha-dash/releases/latest/download/install.sh | sh
#
# Or download it, check it against the release's signed SHA256SUMS, read it,
# then run it (deploy/README.md, "Install"). What it does, in order:
#   1. checks Docker and its Compose plugin are here and answering;
#   2. makes the folder (./chadash, or --dir);
#   3. fetches deploy/compose.yaml from this release's tag, never from main;
#   4. checks the images' signatures with cosign, when cosign is installed,
#      and says so when it isn't;
#   5. pins the release in compose.override.yaml (one it wrote before is
#      updated; one you wrote is left alone);
#   6. starts it (docker compose up -d) and prints the address.
#
# Options: --dir DIR, --tag vX.Y.Z (a release built this script with its own
# tag), --dry-run (print each step, change nothing), --no-start, --help.
# The same as CHADASH_DIR and CHADASH_TAG in the environment.
#
# POSIX sh, so it runs under dash, busybox and bash alike; shellcheck-clean.
set -eu

# The release workflow writes its tag here (release.yml, "Check and name the
# binaries"); in a checkout it stays a placeholder and --tag is required.
STAMPED_TAG='__CHADASH_RELEASE_TAG__'

REPO='ban-red/cha-dash'
IMAGE='ghcr.io/ban-red/chadash'
NODE_IMAGE='ghcr.io/ban-red/chadash-node'
# What the release workflow's signatures name: that workflow, at a v tag,
# through GitHub's OIDC issuer. The same as deploy/README.md shows.
IDENTITY_RE='^https://github\.com/ban-red/cha-dash/\.github/workflows/release\.yml@refs/tags/v'
ISSUER='https://token.actions.githubusercontent.com'
PORT=3696
MARK='# Written by Cha Dash install.sh'

say() { printf '%s\n' "$*"; }
warn() { printf 'install.sh: %s\n' "$*" >&2; }
die() {
	warn "$*"
	exit 1
}

# run prints a command in a dry run, and runs it otherwise.
run() {
	if [ "$DRY_RUN" = 1 ]; then
		say "+ $*"
		return 0
	fi
	"$@"
}

usage() {
	sed -n '2,21p' "$0" 2>/dev/null | sed 's/^# \{0,1\}//' || true
}

# valid_tag reports whether $1 is a release tag: v1.2.3 or v1.2.3-beta.1.
valid_tag() {
	printf '%s\n' "$1" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$'
}

# version_of is a tag without its v: the image tag.
version_of() { printf '%s\n' "${1#v}"; }

# compose_url is deploy/compose.yaml at the tag.
compose_url() { printf 'https://raw.githubusercontent.com/%s/%s/deploy/compose.yaml\n' "$REPO" "$1"; }

# override_for prints compose.override.yaml pinning both images to version $1.
override_for() {
	cat <<EOF
$MARK for $1. It updates this file when you run it
# again; delete this first line to keep your own changes from it.
services:
  chadash:
    image: $IMAGE:$1
  chadash-node:
    image: $NODE_IMAGE:$1
EOF
}

# fetch downloads $1 to $2 with curl or wget.
fetch() {
	if command -v curl >/dev/null 2>&1; then
		run curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		run wget -q -O "$2" "$1"
	else
		die "neither curl nor wget is installed, so the compose file can't be fetched."
	fi
}

check_docker() {
	command -v docker >/dev/null 2>&1 || die "Docker isn't installed. Install Docker Engine with the Compose plugin first: https://docs.docker.com/engine/install/"
	docker compose version >/dev/null 2>&1 || die "Docker's Compose plugin isn't installed (docker compose version failed). Install docker-compose-plugin, then run this again."
	if ! docker info >/dev/null 2>&1; then
		die "Docker isn't answering. Start it, or run this as a user in the docker group (or with sudo)."
	fi
}

# verify_images checks both images' signatures with cosign, if it's here.
verify_images() {
	if ! command -v cosign >/dev/null 2>&1; then
		say "cosign isn't installed, so the images' signatures weren't checked. To check them: https://github.com/$REPO/blob/$TAG/deploy/README.md#checking-an-image"
		return 0
	fi
	for img in "$IMAGE:$VERSION" "$NODE_IMAGE:$VERSION"; do
		say "Checking the signature of $img with cosign"
		if [ "$DRY_RUN" = 1 ]; then
			run cosign verify "$img" --certificate-identity-regexp "$IDENTITY_RE" --certificate-oidc-issuer "$ISSUER"
		elif ! cosign verify "$img" --certificate-identity-regexp "$IDENTITY_RE" --certificate-oidc-issuer "$ISSUER" >/dev/null; then
			die "cosign couldn't verify $img: nothing was started. Don't run an image whose signature doesn't check out."
		fi
	done
	say "Both images are signed by Cha Dash's release workflow."
}

# write_override pins the release, unless the owner wrote the file.
write_override() {
	f="$DIR/compose.override.yaml"
	if [ -f "$f" ] && ! head -n 1 "$f" | grep -qF "$MARK"; then
		warn "$f is yours, so it was left alone. To pin this release, set image: $IMAGE:$VERSION and $NODE_IMAGE:$VERSION in it."
		return 0
	fi
	if [ "$DRY_RUN" = 1 ]; then
		say "+ write $f:"
		override_for "$VERSION" | sed 's/^/    /'
		return 0
	fi
	override_for "$VERSION" >"$f"
}

# address guesses the URL people open: the host's first address.
address() {
	host=''
	if command -v hostname >/dev/null 2>&1; then
		host=$(hostname -I 2>/dev/null | awk '{print $1}') || host=''
		[ -n "$host" ] || host=$(hostname 2>/dev/null) || host=''
	fi
	[ -n "$host" ] || host='<this host>'
	printf 'http://%s:%s\n' "$host" "$PORT"
}

main() {
	DIR=${CHADASH_DIR:-./chadash}
	TAG=${CHADASH_TAG:-}
	DRY_RUN=0
	START=1
	while [ $# -gt 0 ]; do
		case $1 in
		--dir)
			[ $# -ge 2 ] || die "--dir needs a folder"
			DIR=$2
			shift 2
			;;
		--dir=*)
			DIR=${1#--dir=}
			shift
			;;
		--tag)
			[ $# -ge 2 ] || die "--tag needs a release tag, such as v0.4.0"
			TAG=$2
			shift 2
			;;
		--tag=*)
			TAG=${1#--tag=}
			shift
			;;
		--dry-run)
			DRY_RUN=1
			shift
			;;
		--no-start)
			START=0
			shift
			;;
		-h | --help)
			usage
			exit 0
			;;
		*) die "unknown option $1 (try --help)" ;;
		esac
	done
	if [ -z "$TAG" ]; then
		case $STAMPED_TAG in
		__*) die "this copy of install.sh isn't from a release, so it doesn't know which to install. Pass --tag v0.4.0, or use the one attached to a release." ;;
		*) TAG=$STAMPED_TAG ;;
		esac
	fi
	valid_tag "$TAG" || die "$TAG isn't a release tag, such as v0.4.0"
	VERSION=$(version_of "$TAG")

	say "Installing Cha Dash $VERSION into $DIR"
	[ "$DRY_RUN" = 1 ] && say "(a dry run: nothing is changed)"
	check_docker
	run mkdir -p "$DIR"
	fetch "$(compose_url "$TAG")" "$DIR/compose.yaml"
	verify_images
	write_override
	if [ "$START" = 1 ]; then
		run docker compose --project-directory "$DIR" up -d
	else
		say "Not started (--no-start). Start it with: docker compose --project-directory $DIR up -d"
	fi

	say ""
	say "Cha Dash $VERSION: open $(address)"
	say ""
	say "Create the owner account now. Until it exists, anyone who can reach this"
	say "host on port $PORT can create it and take over the dashboard."
}

main "$@"
