#!/usr/bin/env bash
# shellcheck disable=SC1090,SC2034
# A draft for community-scripts/ProxmoxVE (ct/chadash.sh), in their style:
# it runs on the Proxmox VE host, makes a Debian LXC and runs
# install/chadash-install.sh inside it. Not submitted; see ../README.md.
source <(curl -fsSL https://raw.githubusercontent.com/community-scripts/ProxmoxVE/main/misc/build.func)
# Copyright (c) 2021-2026 community-scripts ORG
# Author: ban-red
# License: MIT | https://github.com/community-scripts/ProxmoxVE/raw/main/LICENSE
# Source: https://github.com/ban-red/cha-dash

APP="Cha Dash"
var_tags="${var_tags:-dashboard;homelab;docker}"
var_cpu="${var_cpu:-1}"
var_ram="${var_ram:-1024}"
var_disk="${var_disk:-6}"
var_os="${var_os:-debian}"
var_version="${var_version:-13}"
var_unprivileged="${var_unprivileged:-1}"

header_info "$APP"
variables
color
catch_errors

function update_script() {
  header_info
  check_container_storage
  check_container_resources
  if [[ ! -f /opt/chadash/compose.yaml ]]; then
    msg_error "No ${APP} Installation Found!"
    exit
  fi
  # The newest release's own installer, in the same folder: it fetches that
  # release's compose.yaml, checks the images when cosign is there, moves the
  # pin it wrote in compose.override.yaml, and recreates what changed.
  RELEASE=$(curl -fsSL https://api.github.com/repos/ban-red/cha-dash/releases/latest | jq -r '.tag_name')
  if [[ ! "$RELEASE" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    msg_error "Couldn't read the latest release of ${APP}"
    exit
  fi
  msg_info "Updating ${APP} to ${RELEASE}"
  curl -fsSL "https://github.com/ban-red/cha-dash/releases/download/${RELEASE}/install.sh" -o /tmp/chadash-install.sh
  $STD sh /tmp/chadash-install.sh --dir /opt/chadash --tag "$RELEASE" --no-start
  rm -f /tmp/chadash-install.sh
  cd /opt/chadash || exit
  $STD docker compose pull
  $STD docker compose up -d
  msg_ok "Updated ${APP} to ${RELEASE}"
  exit
}

start
build_container
description

msg_ok "Completed Successfully!\n"
echo -e "${CREATING}${GN}${APP} setup has been successfully initialized!${CL}"
echo -e "${INFO}${YW} Open it now and create the owner account: until it exists, anyone who can reach it can.${CL}"
echo -e "${TAB}${GATEWAY}${BGN}http://${IP}:3696${CL}"
