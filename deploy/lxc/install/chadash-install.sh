#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2154
# A draft for community-scripts/ProxmoxVE (install/chadash-install.sh), in
# their style: it runs inside the new LXC. It installs Docker, then Cha Dash
# the official way, with the release's own install.sh and deploy/compose.yaml
# (the hub and chadash-node, pinned to that release). Not submitted; see
# ../README.md.

# Copyright (c) 2021-2026 community-scripts ORG
# Author: ban-red
# License: MIT | https://github.com/community-scripts/ProxmoxVE/raw/main/LICENSE
# Source: https://github.com/ban-red/cha-dash

source /dev/stdin <<<"$FUNCTIONS_FILE_PATH"
color
verb_ip6
catch_errors
setting_up_container
network_check
update_os

msg_info "Installing Dependencies"
$STD apt-get install -y ca-certificates curl jq
msg_ok "Installed Dependencies"

msg_info "Installing Docker"
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian $(. /etc/os-release && echo "$VERSION_CODENAME") stable" >/etc/apt/sources.list.d/docker.list
$STD apt-get update
$STD apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin
msg_ok "Installed Docker"

msg_info "Installing Cha Dash"
RELEASE=$(curl -fsSL https://api.github.com/repos/ban-red/cha-dash/releases/latest | jq -r '.tag_name')
if [[ ! "$RELEASE" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  msg_error "Couldn't read the latest release of Cha Dash"
  exit 1
fi
curl -fsSL "https://github.com/ban-red/cha-dash/releases/download/${RELEASE}/install.sh" -o /tmp/chadash-install.sh
# install.sh fetches that release's compose.yaml into /opt/chadash, checks
# the images' signatures when cosign is installed (it isn't here; it says
# so), pins the release in compose.override.yaml and starts it.
$STD sh /tmp/chadash-install.sh --dir /opt/chadash --tag "$RELEASE"
rm -f /tmp/chadash-install.sh
msg_ok "Installed Cha Dash ${RELEASE}"

motd_ssh
customize
cleanup_lxc
