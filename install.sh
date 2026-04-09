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
  return 1
}

detect_arch() {
  opkg print-architecture \
    | awk '$1=="arch" && $2!="all" && $2!="noarch"{print $2" "$3}' \
    | sort -k2,2nr \
    | awk 'NR==1{print $1}'
}

install_from_release_assets() {
  API_URL="https://api.github.com/repos/${REPO}/releases/latest"
  TMP_JSON="/tmp/vpn-watchdog-release.json"
  wget -qO "${TMP_JSON}" "${API_URL}" || {
    echo "ERROR: cannot fetch GitHub release metadata: ${API_URL}" >&2
    return 1
  }

  ARCH="$(detect_arch)"
  if [ -z "${ARCH}" ]; then
    echo "ERROR: cannot detect opkg architecture" >&2
    return 1
  fi

  VW_URL="$(grep -Eo 'https://[^"]+vpn-watchdog_[^"]+\.ipk' "${TMP_JSON}" | grep "_${ARCH}\.ipk" | head -n1 || true)"
  LUCI_URL="$(grep -Eo 'https://[^"]+luci-app-vpn-watchdog_[^"]+\.ipk' "${TMP_JSON}" | grep '_all\.ipk' | head -n1 || true)"

  if [ -z "${VW_URL}" ] || [ -z "${LUCI_URL}" ]; then
    echo "ERROR: release assets do not contain required IPKs for arch=${ARCH}" >&2
    echo "Expected assets:" >&2
    echo "  vpn-watchdog_*_${ARCH}.ipk" >&2
    echo "  luci-app-vpn-watchdog_*_all.ipk" >&2
    return 1
  fi

  log "Feed unreachable; falling back to direct release assets install"
  log "Downloading ${VW_URL}"
  wget -qO /tmp/vpn-watchdog.ipk "${VW_URL}"
  log "Downloading ${LUCI_URL}"
  wget -qO /tmp/luci-app-vpn-watchdog.ipk "${LUCI_URL}"

  opkg update
  opkg install /tmp/vpn-watchdog.ipk /tmp/luci-app-vpn-watchdog.ipk
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
  if check_feed_reachable; then
    log "Updating package lists..."
    opkg update
    # Core packages from feed.
    pkg_install vpn-watchdog
    pkg_install luci-app-vpn-watchdog
  else
    log "Feed is not reachable: ${FEED_URL}/Packages.gz"
    if [ "${ALLOW_ASSET_FALLBACK:-1}" = "1" ]; then
      install_from_release_assets || {
        echo "Hint: ensure release has Packages.gz or set FEED_URL to a valid feed." >&2
        exit 1
      }
    else
      echo "ERROR: feed unreachable and fallback disabled (ALLOW_ASSET_FALLBACK=0)" >&2
      exit 1
    fi
  fi

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
