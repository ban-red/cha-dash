#!/usr/bin/env bash
# Spike 4.6 scenarios. Runs ON the sandbox, next to ./node-spike and ./scenarios/.
#
# Resets the sample stack 'hello' to its provisioned baseline, then walks it through adopt → diff →
# six deploys, checking each exit code against what we expect. While each deploy runs, a probe hits
# :8080 every ~250 ms, so the summary also shows how long the site was unreachable.
set -uo pipefail
cd "$(dirname "$0")"

[[ "$(hostname)" == chadash-sandbox ]] || { echo "refusing: this only runs on the chadash-sandbox CT" >&2; exit 1; }

S=./scenarios
STACK=/opt/stacks/hello/compose.yaml
rows=()

step() { printf '\n\033[1m━━ %s\033[0m\n' "$*"; }

probe_start() {
  : >probe.log
  (while :; do
    printf '%s %s\n' "$(date +%s.%N)" "$(curl -s -o /dev/null -m 1 -w '%{http_code}' http://127.0.0.1:8080/)" >>probe.log
    sleep 0.25
  done) &
  PROBE=$!
}

# Seconds the site answered with anything but 200, from the probe's timestamps.
probe_stop() {
  kill "$PROBE" 2>/dev/null
  wait "$PROBE" 2>/dev/null
  awk '{ t = $1 }
       $2 != "200" && !down { down = 1; t0 = t }
       $2 == "200" && down  { down = 0; total += t - t0 }
       END { if (down) total += t - t0; printf "%.1f", total }' probe.log
}

# expect <exit code> <title> <scenario file>
expect() {
  local want="$1" title="$2" file="$3"
  step "$title  (expect exit $want)"
  probe_start
  ./node-spike deploy hello "$file" 2>&1 | tee out.log
  local got=${PIPESTATUS[0]}
  local down; down="$(probe_stop)"
  local rss; rss="$(sed -n 's/^· peak RSS //p' out.log)"
  local verdict=PASS; [[ "$got" == "$want" ]] || verdict=FAIL
  rows+=("$verdict|$title|$want|$got|${down}s|$rss")
}

step "reset: hello back to the provisioned baseline, no stacks repo"
rm -rf /opt/stacks/.git /opt/stacks/.gitignore
cp "$S/00-baseline.yaml" "$STACK"
docker compose -f "$STACK" up -d --remove-orphans --wait --quiet-pull

step "scan"
./node-spike scan

step "adopt hello in place (expect: nothing restarts)"
before="$(docker inspect -f '{{.Id}}' hello-web-1)"
./node-spike adopt hello
after="$(docker inspect -f '{{.Id}}' hello-web-1)"
adopt_kept=FAIL; [[ "$before" == "$after" ]] && adopt_kept=PASS
./node-spike scan

step "diff against the running file (expect: identical, web unchanged; proves our hash matches the CLI's)"
diff_out="$(./node-spike diff hello "$S/00-baseline.yaml")"
echo "$diff_out"
hash_parity=FAIL; grep -q '= web .*unchanged' <<<"$diff_out" && hash_parity=PASS

expect 0 "01 upgrade nginx 1.27 → 1.28"                "$S/01-upgrade.yaml"
expect 2 "02 healthcheck never passes → rollback"     "$S/02-unhealthy.yaml"
expect 2 "03 crash loop --wait can miss → rollback"   "$S/03-crashloop.yaml"
expect 4 "04 image doesn't exist → blocked by dry run" "$S/04-missing-image.yaml"
expect 0 "05 add whoami, web untouched"                "$S/05-add-service.yaml"
expect 0 "06 back to baseline, whoami removed"         "$S/00-baseline.yaml"

step "history"
./node-spike history hello

step "docker compose ls still sees the stack at its real path"
docker compose ls --all

step "summary"
printf '%-6s  %-46s  %4s  %3s  %-11s  %s\n' "" "scenario" "want" "got" "unreachable" "peak RSS"
printf '%-6s  %-46s\n' "$adopt_kept" "adopt keeps the running container"
printf '%-6s  %-46s\n' "$hash_parity" "our config hash matches the CLI's label"
for r in "${rows[@]}"; do
  IFS='|' read -r v t w g d m <<<"$r"
  printf '%-6s  %-46s  %4s  %3s  %-11s  %s\n' "$v" "$t" "$w" "$g" "$d" "$m"
done
printf '\nnode-spike binary: %s\n' "$(du -h node-spike | cut -f1)"
