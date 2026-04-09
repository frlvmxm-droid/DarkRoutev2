#!/bin/sh
# One-command installer for vpn-watchdog on OpenWrt.
# Usage:
#   wget -O - https://raw.githubusercontent.com/frlvmxm-droid/darkroute/main/install.sh | sh
#
# Optional env flags:
#   INSTALL_VLESS=1   -> install sing-box
#   INSTALL_AWG=1     -> install kmod-amnezia-wireguard + awg-tools
#   SKIP_ENABLE=1     -> do not enable/start service automatically

set -eu

REPO="${REPO:-frlvmxm-droid/darkroute}"
FEED_URL="${FEED_URL:-https://github.com/${REPO}/releases/latest/download}"
FEED_LINE="src/gz vpn-watchdog ${FEED_URL}"
CUSTOM_FEEDS="/etc/opkg/customfeeds.conf"

log() {
  echo "[vpn-watchdog install] $*"
}

require_root() {
  if [ "$(id -u)" != "0" ]; then
    echo "ERROR: run as root" >&2
    exit 1
  fi
}

ensure_feed() {
  mkdir -p /etc/opkg
  touch "${CUSTOM_FEEDS}"
  # Keep only one vpn-watchdog feed line to avoid conflicts with old repos.
  sed -i '/^src\/gz[[:space:]]\+vpn-watchdog[[:space:]]\+/d' "${CUSTOM_FEEDS}"
  log "Adding feed to ${CUSTOM_FEEDS}: ${FEED_URL}"
  echo "${FEED_LINE}" >> "${CUSTOM_FEEDS}"
}

check_feed_reachable() {
  if wget -q --spider "${FEED_URL}/Packages.gz"; then
    return 0
  fi
  echo "ERROR: Feed is not reachable: ${FEED_URL}/Packages.gz" >&2
  echo "Hint: if you moved repo, pass FEED_URL explicitly, e.g.:" >&2
  echo "  FEED_URL='https://github.com/<owner>/<repo>/releases/latest/download' \\" >&2
  echo "  wget -O - https://raw.githubusercontent.com/<owner>/<repo>/main/install.sh | sh" >&2
  exit 1
}

pkg_install() {
  PKG="$1"
  if opkg list-installed | awk '{print $1}' | grep -qx "${PKG}"; then
    log "Package already installed: ${PKG}"
    return 0
  fi
  log "Installing package: ${PKG}"
  opkg install "${PKG}"
}

main() {
  require_root

  log "Detected system:"
  ubus call system board 2>/dev/null || true
  opkg print-architecture || true

  ensure_feed
  check_feed_reachable

  log "Updating package lists..."
  opkg update

  # Core packages.
  pkg_install vpn-watchdog
  pkg_install luci-app-vpn-watchdog

  # Optional runtime helpers.
  if [ "${INSTALL_VLESS:-0}" = "1" ]; then
    pkg_install sing-box
  fi
  if [ "${INSTALL_AWG:-0}" = "1" ]; then
    pkg_install kmod-amnezia-wireguard
    pkg_install awg-tools
  fi

  if [ "${SKIP_ENABLE:-0}" != "1" ]; then
    log "Enabling and starting vpn-watchdog..."
    /etc/init.d/vpn-watchdog enable || true
    /etc/init.d/vpn-watchdog restart || /etc/init.d/vpn-watchdog start || true
  fi

  log "Health check:"
  if command -v curl >/dev/null 2>&1; then
    curl -sf --max-time 5 http://127.0.0.1:8765/health 2>/dev/null || true
  else
    wget -qO- http://127.0.0.1:8765/health 2>/dev/null || true
  fi

  log "Done."
  log "Open LuCI: Services -> VPN Watchdog"
}

main "$@"
