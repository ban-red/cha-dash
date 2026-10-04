#!/usr/bin/env bash
# Cross-compiles the hub (with and without the embedded SPA) and the node spike
# for every candidate platform, prints a size table, and deletes the binaries.
#
#   bun run build                 # first, so -tags webui has something to embed
#   spikes/footprint/sizes.sh
#
# Builds are the release flags: CGO_ENABLED=0, -trimpath, -ldflags "-s -w".
# Nothing runs; this only measures.
set -euo pipefail

repo=$(cd "$(dirname "$0")/../.." && pwd)
out="$repo/spikes/footprint/bin"   # git-ignored (spikes/*/bin/)
mkdir -p "$out"
trap 'rm -rf "$out"' EXIT

if [ ! -f "$repo/internal/webui/dist/index.html" ]; then
  echo "sizes.sh: internal/webui/dist is missing; run 'bun run build' first" >&2
  exit 1
fi

# platform label, GOARCH, GOARM
platforms=("amd64 amd64 -" "arm64 arm64 -" "armv7 arm 7")

build() { # label goarch goarm dir output [go build args...]
  local label=$1 goarch=$2 goarm=$3 dir=$4 output=$5
  shift 5
  local env=(CGO_ENABLED=0 GOOS=linux GOARCH="$goarch")
  [ "$goarm" != - ] && env+=(GOARM="$goarm")
  if ! (cd "$dir" && env "${env[@]}" go build -trimpath -ldflags "-s -w" -o "$output" "$@" 2>"$output.log"); then
    echo "FAILED: $label $(basename "$output")" >&2
    cat "$output.log" >&2
    return 1
  fi
  if [ -s "$output.log" ]; then
    echo "warnings for $label $(basename "$output"):" >&2
    cat "$output.log" >&2
  fi
}

mib() { awk -v b="$1" 'BEGIN { printf "%.1f", b / 1048576 }'; }
mb_gz() { awk -v b="$(gzip -6 -c "$1" | wc -c)" 'BEGIN { printf "%.1f", b / 1000000 }'; }

printf '| Platform | Hub (no SPA) | Hub + SPA | Hub + SPA, gzip | Node spike | Node, gzip |\n'
printf '|---|---:|---:|---:|---:|---:|\n'
for p in "${platforms[@]}"; do
  read -r label goarch goarm <<<"$p"
  build "$label" "$goarch" "$goarm" "$repo" "$out/chadash-$label" ./cmd/chadash
  build "$label" "$goarch" "$goarm" "$repo" "$out/chadash-webui-$label" -tags webui ./cmd/chadash
  build "$label" "$goarch" "$goarm" "$repo/spikes/node-compose" "$out/node-$label" .
  hub=$(stat -f %z "$out/chadash-$label" 2>/dev/null || stat -c %s "$out/chadash-$label")
  web=$(stat -f %z "$out/chadash-webui-$label" 2>/dev/null || stat -c %s "$out/chadash-webui-$label")
  node=$(stat -f %z "$out/node-$label" 2>/dev/null || stat -c %s "$out/node-$label")
  printf '| %s | %s MiB | %s MiB | %s MB | %s MiB | %s MB |\n' "$label" \
    "$(mib "$hub")" "$(mib "$web")" "$(mb_gz "$out/chadash-webui-$label")" \
    "$(mib "$node")" "$(mb_gz "$out/node-$label")"
done
