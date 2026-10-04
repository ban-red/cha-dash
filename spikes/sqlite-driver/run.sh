#!/usr/bin/env bash
# Spike 4.8: modernc vs ncruces. Local only: no servers, no Docker, no network
# beyond the Go module proxy. Every binary is built into a temp dir and deleted.
#
#   ./run.sh test        correctness and encryption checks
#   ./run.sh bench       workload benchmarks (5 runs each, -cpu 4)
#   ./run.sh footprint   RSS, first query, startup, mixed load (child process per driver)
#   ./run.sh connmem     memory the pool keeps after large layouts, and after idle readers close
#   ./run.sh size        binary size of a tiny main per driver, and of the hub itself
#   ./run.sh deps        dependency tree per driver
#   ./run.sh hubtests    the hub's own tests with internal/store swapped to ncruces
#   ./run.sh all
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cd "$here"

# The hub with internal/store/driver_modernc.go replaced by hubswap/driver.go,
# through -overlay and a temporary -modfile: nothing in the repo changes.
swap_flags() {
  cp "$root/go.mod" "$root/go.sum" "$tmp/"
  local ver
  ver=$(go list -m -f '{{.Version}}' github.com/ncruces/go-sqlite3)
  go mod edit -modfile="$tmp/go.mod" -require="github.com/ncruces/go-sqlite3@$ver"
  printf '{"Replace":{"%s":"%s"}}\n' "$root/internal/store/driver_modernc.go" "$here/hubswap/driver.go" >"$tmp/overlay.json"
  echo "-mod=mod -modfile=$tmp/go.mod -overlay=$tmp/overlay.json"
}

kib() { echo $(( ($1 + 512) / 1024 )); }

cmd_test() {
  go test -count=1 ./...
}

cmd_bench() {
  go test -run '^$' -bench . -benchtime 1s -count 5 -cpu 4 .
}

cmd_footprint() {
  SPIKE_FOOTPRINT=1 SPIKE_ROUNDS=${SPIKE_ROUNDS:-5} go test -count=1 -run '^TestFootprint$' -v . | grep -v '^=== RUN'
}

cmd_connmem() {
  SPIKE_CONNMEM=1 SPIKE_ROUNDS=${SPIKE_ROUNDS:-3} go test -count=1 -run '^TestConnMemory$' -v . | grep -E '^(\||Medians)'
}

cmd_size() {
  echo "Stripped (-s -w), -trimpath, CGO_ENABLED=0. KiB; gz = gzip -9. arm is GOARM=7, for ADR-0016."
  for arch in amd64 arm64 arm; do
    echo
    echo "linux/$arch"
    printf '%-18s %8s %8s %8s %8s\n' main size added gz added
    local base=0 basegz=0
    for m in baseline modernc ncruces ncruces-adiantum; do
      CGO_ENABLED=0 GOOS=linux GOARCH=$arch GOARM=7 go build -trimpath -ldflags='-s -w' -o "$tmp/$m" "./cmd/size/$m"
      gzip -9 -k "$tmp/$m"
      local s gz
      s=$(wc -c <"$tmp/$m"); gz=$(wc -c <"$tmp/$m.gz")
      [ "$m" = baseline ] && base=$s basegz=$gz
      printf '%-18s %8s %8s %8s %8s\n' "$m" "$(kib "$s")" "+$(kib $((s - base)))" "$(kib "$gz")" "+$(kib $((gz - basegz)))"
      rm -f "$tmp/$m" "$tmp/$m.gz"
    done
  done

  local flags
  flags=$(swap_flags)
  echo
  echo "The hub (cmd/chadash, placeholder web UI)"
  printf '%-18s %8s %8s %8s\n' build modernc ncruces delta
  for arch in amd64 arm64; do
    (cd "$root" && CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags='-s -w' -o "$tmp/hub-m" ./cmd/chadash)
    # shellcheck disable=SC2086
    (cd "$root" && CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build $flags -trimpath -ldflags='-s -w' -o "$tmp/hub-n" ./cmd/chadash)
    local m n
    m=$(wc -c <"$tmp/hub-m"); n=$(wc -c <"$tmp/hub-n")
    printf '%-18s %8s %8s %8s\n' "linux/$arch" "$(kib "$m")" "$(kib "$n")" "$(kib $((n - m)))"
    rm -f "$tmp/hub-m" "$tmp/hub-n"
  done
}

cmd_deps() {
  for m in modernc ncruces ncruces-adiantum; do
    local pkgs mods
    pkgs=$(go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' "./cmd/size/$m" | grep -vc "cmd/size")
    mods=$(go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}}@{{.Version}}{{end}}{{end}}' "./cmd/size/$m" | sort -u)
    echo "$m: $pkgs non-std packages, $(echo "$mods" | grep -c .) modules"
    echo "$mods" | sed 's/^/    /'
  done
}

cmd_hubtests() {
  local flags
  flags=$(swap_flags)
  echo "The hub's tests, internal/store on ncruces (overlay: hubswap/driver.go)."
  echo -n "internal/store links: "
  # shellcheck disable=SC2086
  (cd "$root" && go list $flags -deps ./internal/store | grep -E '^(modernc.org/sqlite|github.com/ncruces/go-sqlite3/driver)$')
  # shellcheck disable=SC2086
  (cd "$root" && go test $flags -count=1 ./internal/... ./cmd/...)
}

case "${1:-all}" in
  test) cmd_test ;;
  bench) cmd_bench ;;
  footprint) cmd_footprint ;;
  connmem) cmd_connmem ;;
  size) cmd_size ;;
  deps) cmd_deps ;;
  hubtests) cmd_hubtests ;;
  all) cmd_test; cmd_hubtests; cmd_deps; cmd_size; cmd_footprint; cmd_connmem; cmd_bench ;;
  *) sed -n '2,12p' "$0"; exit 2 ;;
esac
