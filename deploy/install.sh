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
#   3. fetches deploy/compose.yaml from this release's tag, never from main,
#      and checks it against the hash the release stamped in this script;
#   4. checks the images' signatures with cosign, when cosign is installed,
#      and says so when it isn't: signed by this release's workflow run;
#   5. pins the release in compose.override.yaml, by the digest cosign
#      verified when it could (one it wrote before is updated; one you wrote
#      is left alone);
#   6. starts it (docker compose up -d) and prints the address.
#
# Options: --dir DIR, --tag vX.Y.Z (a release built this script with its own
# tag), --dry-run (print each step, change nothing), --no-start, --help.
# The same as CHADASH_DIR and CHADASH_TAG in the environment.
#
# POSIX sh, so it runs under dash, busybox and bash alike; shellcheck-clean.
set -eu

# The release workflow writes its tag here (release-build.yml, "Check and name the
# binaries"); in a checkout it stays a placeholder and --tag is required.
STAMPED_TAG='__CHADASH_RELEASE_TAG__'
# And deploy/compose.yaml's SHA-256 at that tag, so the file fetched from
# GitHub is the one the release was built with. This script is in the
# release's signed SHA256SUMS, so the signature covers the hash.
STAMPED_COMPOSE_SHA256='__CHADASH_COMPOSE_SHA256__'

REPO='ban-red/cha-dash'
IMAGE='ghcr.io/ban-red/chadash'
NODE_IMAGE='ghcr.io/ban-red/chadash-node'
# What the release workflow's signatures name: that workflow, at a tag,
# through GitHub's OIDC issuer. The same as deploy/README.md shows;
# identity_re anchors it to the one tag being installed, so an older
# release's signed image under a moved tag doesn't pass.
IDENTITY_PREFIX='^https://github\.com/ban-red/cha-dash/\.github/workflows/release\.yml@refs/tags/'
HUB_DIGEST=''
NODE_DIGEST=''
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

# identity_re is the signer cosign must see for tag $1, exactly.
identity_re() { printf '%s%s$\n' "$IDENTITY_PREFIX" "$(printf '%s' "$1" | sed 's/\./\\./g')"; }

# sha256_of prints file $1's SHA-256, or nothing without a tool for it.
sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

# version_of is a tag without its v: the image tag.
version_of() { printf '%s\n' "${1#v}"; }

# compose_url is deploy/compose.yaml at the tag.
compose_url() { printf 'https://raw.githubusercontent.com/%s/%s/deploy/compose.yaml\n' "$REPO" "$1"; }

# override_for prints compose.override.yaml pinning both images to version
# $1, and to the digests cosign verified when it ran.
override_for() {
	cat <<EOF
$MARK for $1. It updates this file when you run it
# again; delete this first line to keep your own changes from it.
services:
  chadash:
    image: $IMAGE:$1$HUB_DIGEST
  chadash-node:
    image: $NODE_IMAGE:$1$NODE_DIGEST
EOF
}

# fetch downloads $1 to $2 with curl or wget.
fetch() {
	if command -v curl >/dev/null 2>&1; then
		run curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		# GNU wget can refuse a redirect to plain HTTP; busybox's has no flag.
		if wget --help 2>&1 | grep -q -- '--https-only'; then
			run wget -q --https-only -O "$2" "$1"
		else
			run wget -q -O "$2" "$1"
		fi
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
	# The release signs with cosign 3, which keeps signatures as OCI
	# referrers; cosign 2 looks for .sig tags, finds none and says the image
	# is unsigned. Ask for 3 rather than refuse a signed image.
	major=$(cosign version 2>/dev/null | sed -n 's/^GitVersion: *v\{0,1\}\([0-9][0-9]*\)\..*/\1/p' | head -n 1)
	if [ -n "$major" ] && [ "$major" -lt 3 ]; then
		die "cosign $major is installed; checking Cha Dash's signatures needs cosign 3 or later (the release signs with its bundle format). Update it, or uninstall it to skip the check: nothing was started."
	fi
	re=$(identity_re "$TAG")
	for img in "$IMAGE:$VERSION" "$NODE_IMAGE:$VERSION"; do
		say "Checking the signature of $img with cosign"
		if [ "$DRY_RUN" = 1 ]; then
			run cosign verify "$img" --certificate-identity-regexp "$re" --certificate-oidc-issuer "$ISSUER"
			continue
		fi
		if ! out=$(cosign verify "$img" --certificate-identity-regexp "$re" --certificate-oidc-issuer "$ISSUER" 2>/dev/null); then
			die "cosign couldn't verify $img: nothing was started. Don't run an image whose signature doesn't check out."
		fi
		# Pin what was verified: Compose then pulls that digest, not
		# whatever the tag points to by then.
		digest=$(printf '%s\n' "$out" | grep -o '"docker-manifest-digest": *"sha256:[0-9a-f]\{64\}"' | head -n 1 | grep -o 'sha256:[0-9a-f]*') || digest=''
		[ -n "$digest" ] || die "cosign verified $img but didn't name its digest, so it can't be pinned: nothing was started."
		case $img in
		"$IMAGE:"*) HUB_DIGEST="@$digest" ;;
		*) NODE_DIGEST="@$digest" ;;
		esac
	done
	say "Both images are signed by Cha Dash's release workflow for $TAG, and pinned by digest."
}

# check_compose compares the fetched compose.yaml with the stamped hash,
# when this script is the release's own and installs that release.
check_compose() {
	case $STAMPED_COMPOSE_SHA256 in
	__*) return 0 ;;
	esac
	if [ "$TAG" != "$STAMPED_TAG" ]; then
		say "compose.yaml wasn't checked against a hash: this installer is from $STAMPED_TAG, installing $TAG."
		return 0
	fi
	if [ "$DRY_RUN" = 1 ]; then
		say "+ check $DIR/compose.yaml has SHA-256 $STAMPED_COMPOSE_SHA256"
		return 0
	fi
	got=$(sha256_of "$DIR/compose.yaml")
	[ -n "$got" ] || die "neither sha256sum nor shasum is installed, so compose.yaml can't be checked: nothing was started."
	if [ "$got" != "$STAMPED_COMPOSE_SHA256" ]; then
		rm -f "$DIR/compose.yaml"
		die "compose.yaml from GitHub isn't the one $TAG was released with (SHA-256 $got): nothing was started."
	fi
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
	check_compose
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
