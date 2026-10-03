#!/usr/bin/env bash
# ▶ You run this (or tell me to): cross-compile the spike, copy it to the sandbox, run the scenarios.
# Needs your SSH key on the sandbox (see ../node-sandbox/README.md#ssh-permission-denied-publickey).
set -euo pipefail
cd "$(dirname "$0")"

HOST="${NODE_SANDBOX_HOST:-$(sed -n 's/^NODE_SANDBOX_HOST=//p' ../../.env.local 2>/dev/null)}"
[[ -n "$HOST" ]] || { echo "Set NODE_SANDBOX_HOST, or put it in .env.local" >&2; exit 1; }

ARCH="$(ssh "root@$HOST" dpkg --print-architecture)"
echo "▸ Building for linux/$ARCH"
GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/node-spike .
echo "  $(du -h bin/node-spike | cut -f1) · $(go list -deps . | grep -c '\.') packages · $(go list -m all | wc -l | xargs) modules"

echo "▸ Copying to $HOST:/root/node-spike"
ssh "root@$HOST" 'mkdir -p /root/node-spike'
scp -q bin/node-spike run-scenarios.sh "root@$HOST:/root/node-spike/"
scp -qr scenarios "root@$HOST:/root/node-spike/"

log="bin/run-$(date +%Y%m%d-%H%M%S).log"
ssh "root@$HOST" /root/node-spike/run-scenarios.sh 2>&1 | tee "$log"
echo "▸ Log saved to spikes/node-compose/$log"
