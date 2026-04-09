#!/usr/bin/env bash
set -euo pipefail

# Build vpn-watchdog + luci-app-vpn-watchdog IPKs for
# Cudy TR3000 v1 / OpenWrt 24.10.x / mediatek/filogic
#
# Usage:
#   ./scripts/build_tr3000_filogic.sh [OPENWRT_VERSION]
# Example:
#   ./scripts/build_tr3000_filogic.sh 24.10.5

OPENWRT_VERSION="${1:-24.10.5}"
TARGET_URL="https://downloads.openwrt.org/releases/${OPENWRT_VERSION}/targets/mediatek/filogic"
SDK_PAGE="$(mktemp)"

echo "[1/8] Looking up SDK URL for OpenWrt ${OPENWRT_VERSION}..."
curl -fsSL "${TARGET_URL}/" -o "${SDK_PAGE}"

SDK_FILE="$(grep -o 'openwrt-sdk-[^"]*\.tar\.xz' "${SDK_PAGE}" | head -n1 || true)"
if [[ -z "${SDK_FILE}" ]]; then
  echo "ERROR: Could not find SDK archive on ${TARGET_URL}/" >&2
  exit 1
fi

echo "[2/8] Downloading SDK: ${SDK_FILE}"
curl -fL "${TARGET_URL}/${SDK_FILE}" -o "${SDK_FILE}"

echo "[3/8] Extracting SDK..."
tar -xJf "${SDK_FILE}"
SDK_DIR="$(tar -tf "${SDK_FILE}" | head -n1 | cut -d/ -f1)"
if [[ -z "${SDK_DIR}" || ! -d "${SDK_DIR}" ]]; then
  echo "ERROR: Failed to detect extracted SDK directory" >&2
  exit 1
fi

echo "[4/8] Entering ${SDK_DIR}"
cd "${SDK_DIR}"

echo "[5/8] Adding vpn-watchdog feed..."
echo "src-git vpn-watchdog https://github.com/frlvmxm-droid/darkroute.git;main" >> feeds.conf.default

echo "[6/8] Updating/installing feeds..."
./scripts/feeds update vpn-watchdog packages
./scripts/feeds install -a -p vpn-watchdog
./scripts/feeds install golang

echo "[7/8] Building packages..."
make package/vpn-watchdog/compile V=s
make package/luci-app-vpn-watchdog/compile V=s

echo "[8/8] Build complete."
echo "IPK output:"
echo "  ${PWD}/bin/packages/*/vpn-watchdog/"
echo ""
echo "Copy to router and install:"
echo "  opkg install ./vpn-watchdog_*.ipk ./luci-app-vpn-watchdog_*.ipk"
