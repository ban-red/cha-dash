#!/usr/bin/env bash
# Cha Dash · node spike sandbox (spike 4.6)
#
# Creates a throwaway, unprivileged Debian LXC with Docker + Compose on THIS Proxmox VE host,
# for testing chadash-node: compose deploy → diff → health gate → rollback.
# The sandbox always uses CT ID 3696 (same as Cha Dash's port), is tagged "chadash;sandbox",
# never autostarts, and is safe to destroy.
#
# Run on the PVE host as root (recommended host: mars-2):
#   ./sandbox.sh create          # create + provision (refuses if the sandbox or the CT ID already exists)
#   ./sandbox.sh status          # show CT ID, state, IP
#   ./sandbox.sh destroy         # stop + destroy (asks you to type the hostname)
#
# Settings (override via environment):
#   VMID=3696  CT_HOSTNAME=chadash-sandbox  CORES=2  MEMORY=2048  SWAP=512  DISK_GB=16
#   BRIDGE=vmbr0  VLAN=  STORAGE=<auto: first active rootdir storage>  TEMPLATE_STORAGE=local
#   FEATURES=nesting=1,keyctl=1  SSH_KEYS=/root/.ssh/authorized_keys
set -euo pipefail

VMID="${VMID:-3696}"
CT_HOSTNAME="${CT_HOSTNAME:-chadash-sandbox}"
CORES="${CORES:-2}"
MEMORY="${MEMORY:-2048}"
SWAP="${SWAP:-512}"
DISK_GB="${DISK_GB:-16}"
BRIDGE="${BRIDGE:-vmbr0}"
VLAN="${VLAN:-}"
STORAGE="${STORAGE:-}"
TEMPLATE_STORAGE="${TEMPLATE_STORAGE:-local}"
FEATURES="${FEATURES:-nesting=1,keyctl=1}"
SSH_KEYS="${SSH_KEYS:-/root/.ssh/authorized_keys}"

say()  { printf '\033[1m▸ %s\033[0m\n' "$*"; }
warn() { printf '\033[33m▲ %s\033[0m\n' "$*" >&2; }
die()  { printf '\033[31m■ %s\033[0m\n' "$*" >&2; exit 1; }

preflight() {
  [[ $EUID -eq 0 ]] || die "run as root on the Proxmox VE host"
  for c in pct pveam pvesm pvesh; do command -v "$c" >/dev/null || die "'$c' not found. Is this a Proxmox VE host?"; done
}

# Local CT with our hostname, if any. `pct list` columns: VMID Status [Lock] Name. Name is the last field.
find_vmid() { pct list | awk -v n="$CT_HOSTNAME" 'NR>1 && $NF==n {print $1; exit}'; }

ct_ip() { pct exec "$1" -- sh -c "ip -4 -o addr show eth0 2>/dev/null | awk '{print \$4}' | cut -d/ -f1" 2>/dev/null || true; }

bridges() { ip -br link show type bridge 2>/dev/null | awk '{print $1}' | xargs; }

# Start the CT; on failure, collect the LXC debug log and point at the likely cause.
start_or_explain() {
  local log; log="$(mktemp /tmp/chadash-sandbox-start.XXXXXX.log)"
  if pct start "$VMID" 2>"$log"; then rm -f "$log"; return 0; fi
  warn "CT $VMID failed to start. Collecting the LXC debug log…"
  pct start "$VMID" --debug >>"$log" 2>&1 || true
  local errs; errs="$(grep -E '^(ERROR|WARN) ' "$log" || true)"
  echo "──── errors and warnings (full log: $log) ────"
  if [[ -n "$errs" ]]; then echo "$errs" | tail -n 20; else tail -n 20 "$log"; fi
  echo "─────────────────────────────────────────────"
  grep -qi 'Exec format error' <<<"$errs"       && warn "Architecture mismatch: the template doesn't match this host ($(dpkg --print-architecture))."
  grep -qiE 'bridge|veth|netdev' <<<"$errs"     && warn "Network/bridge problem. Bridges here: $(bridges). Re-run with BRIDGE=<name>."
  grep -qiE 'apparmor|denied' <<<"$errs"        && warn "AppArmor/permission problem. See Troubleshooting in spikes/node-sandbox/README.md."
  grep -qi 'keyctl' <<<"$errs"                  && warn "keyctl problem. Re-run with FEATURES=nesting=1."
  grep -qiE 'newuidmap|idmap|uid_map' <<<"$errs" && warn "UID mapping problem. Check that /etc/subuid and /etc/subgid contain 'root:100000:65536'."
  die "start failed. CT $VMID is left in place for inspection; remove it with: $0 destroy"
}

create() {
  preflight
  say "Host $(hostname) · $(dpkg --print-architecture) · PVE $(pveversion | awk -F/ '{print $2}')"

  local existing; existing="$(find_vmid)"
  [[ -z "$existing" ]] || die "a container named '$CT_HOSTNAME' already exists (CT $existing). Remove it first: $0 destroy"
  pvesh get /cluster/nextid --vmid "$VMID" >/dev/null 2>&1 \
    || die "CT ID $VMID is already in use somewhere in this cluster. Pick another: VMID=<id> $0 create"
  ip link show "$BRIDGE" >/dev/null 2>&1 || die "bridge '$BRIDGE' not found on this host. Available: $(bridges). Re-run with BRIDGE=<name>"

  if [[ -z "$STORAGE" ]]; then
    STORAGE="$(pvesm status --content rootdir | awk 'NR>1 && $3=="active" {print $1; exit}')"
    [[ -n "$STORAGE" ]] || die "no active storage with 'rootdir' content found; set STORAGE=..."
  fi

  local arch; arch="$(dpkg --print-architecture)"
  say "Finding the latest Debian standard template for $arch"
  pveam update >/dev/null || warn "pveam update failed; using the cached template list"
  # The appliance index lists every architecture; pick only templates built for this host.
  pick() { pveam available --section system | awk -v re="^debian-$1-standard_.*_${arch}\\.tar" '$2 ~ re {print $2}' | sort -V | tail -1; }
  local tpl; tpl="$(pick 13)"
  [[ -n "$tpl" ]] || tpl="$(pick 12)"
  [[ -n "$tpl" ]] || die "no Debian standard template for $arch available"
  if ! pveam list "$TEMPLATE_STORAGE" | grep -q "$tpl"; then
    say "Downloading $tpl to $TEMPLATE_STORAGE"
    pveam download "$TEMPLATE_STORAGE" "$tpl"
  fi

  local net="name=eth0,bridge=${BRIDGE},ip=dhcp,ip6=auto"
  [[ -n "$VLAN" ]] && net+=",tag=${VLAN}"
  local ssh_args=()
  [[ -s "$SSH_KEYS" ]] && ssh_args=(--ssh-public-keys "$SSH_KEYS")

  say "Creating CT $VMID ($CT_HOSTNAME) on $STORAGE · bridge $BRIDGE · ${CORES} cores · ${MEMORY} MB · ${DISK_GB} GB · features $FEATURES"
  pct create "$VMID" "${TEMPLATE_STORAGE}:vztmpl/${tpl}" \
    --hostname "$CT_HOSTNAME" \
    --cores "$CORES" --memory "$MEMORY" --swap "$SWAP" \
    --rootfs "${STORAGE}:${DISK_GB}" \
    --net0 "$net" \
    --unprivileged 1 \
    --features "$FEATURES" \
    --onboot 0 \
    --tags "chadash;sandbox" \
    --description "Cha Dash node spike sandbox (spike 4.6). Throwaway: safe to destroy." \
    "${ssh_args[@]}"

  say "Starting and waiting for network"
  start_or_explain
  local ip=""
  for _ in $(seq 1 60); do ip="$(ct_ip "$VMID")"; [[ -n "$ip" ]] && break; sleep 1; done
  [[ -n "$ip" ]] || die "CT $VMID started but has no IPv4 address after 60s (check DHCP on '$BRIDGE'${VLAN:+ VLAN $VLAN})"

  say "Provisioning Docker Engine + Compose plugin (Docker's official apt repo)"
  local prov; prov="$(mktemp)"
  cat >"$prov" <<'EOS'
#!/usr/bin/env bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
# SSH forwards your Mac's LANG (e.g. en_US.UTF-8), which the minimal template doesn't have; C.UTF-8 always exists.
export LANG=C.UTF-8 LC_ALL=C.UTF-8
apt-get update -qq
apt-get install -y -qq ca-certificates curl git >/dev/null
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
chmod a+r /etc/apt/keyrings/docker.asc
. /etc/os-release
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian ${VERSION_CODENAME} stable" \
  > /etc/apt/sources.list.d/docker.list
apt-get update -qq
apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-compose-plugin >/dev/null
systemctl enable --now docker >/dev/null

# A running compose project for the node to detect and "adopt in place".
mkdir -p /opt/stacks/hello
cat > /opt/stacks/hello/compose.yaml <<'YAML'
name: hello
services:
  web:
    image: nginx:1.27-alpine
    ports: ["8080:80"]
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1/"]
      interval: 10s
      timeout: 3s
      retries: 3
YAML
docker compose -f /opt/stacks/hello/compose.yaml up -d --quiet-pull
docker --version
docker compose version
EOS
  pct push "$VMID" "$prov" /root/provision.sh --perms 0755
  rm -f "$prov"
  if ! pct exec "$VMID" -- /root/provision.sh; then
    warn "provisioning failed. If containers fail to start with an AppArmor or sysctl 'permission denied' error,"
    warn "see the Troubleshooting section in spikes/node-sandbox/README.md"
    exit 1
  fi

  local arch; arch="$(pct exec "$VMID" -- dpkg --print-architecture)"
  cat <<EOF

✓ Sandbox ready
  CT ID     $VMID   (tags: chadash;sandbox · onboot: no)
  Hostname  $CT_HOSTNAME
  Arch      $arch   → build the node with GOOS=linux GOARCH=$arch
  IPv4      $ip
  Stacks    /opt/stacks   (sample project 'hello' running → http://$ip:8080)
  SSH       ssh root@$ip   $( [[ ${#ssh_args[@]} -gt 0 ]] && echo "(keys copied from this host's $SSH_KEYS)" || echo "(no keys injected; use: pct enter $VMID)" )
            Key not accepted? From your Mac:
            ssh root@$(hostname) "pct exec $VMID -- sh -c 'cat >> /root/.ssh/authorized_keys'" < ~/.ssh/id_ed25519.pub

  Next: put NODE_SANDBOX_HOST=$ip in .env.local on your Mac.
  Teardown: $0 destroy
EOF
}

status() {
  preflight
  local id; id="$(find_vmid)"
  [[ -n "$id" ]] || { echo "no '$CT_HOSTNAME' container on this host"; exit 0; }
  local st; st="$(pct status "$id" | awk '{print $2}')"
  echo "CT $id · $st · ${CT_HOSTNAME} · IPv4 $( [[ $st == running ]] && ct_ip "$id" || echo '-')"
}

destroy() {
  preflight
  local id; id="$(find_vmid)"
  [[ -n "$id" ]] || die "no '$CT_HOSTNAME' container on this host"
  pct config "$id" | grep -q '^tags:.*sandbox' || die "CT $id is not tagged 'sandbox'; refusing to destroy it"
  echo "This permanently destroys CT $id ($CT_HOSTNAME) and its disk."
  read -r -p "Type the hostname to confirm: " answer
  [[ "$answer" == "$CT_HOSTNAME" ]] || die "confirmation did not match; nothing destroyed"
  pct stop "$id" >/dev/null 2>&1 || true
  pct destroy "$id" --purge
  say "Destroyed CT $id"
}

case "${1:-}" in
  create)  create ;;
  status)  status ;;
  destroy) destroy ;;
  *) sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//'; exit 1 ;;
esac
