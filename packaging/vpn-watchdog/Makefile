# OpenWrt package Makefile for vpn-watchdog daemon.
# Place this file at:
#   feeds/vpn-watchdog/vpn-watchdog/Makefile
# in your OpenWrt build tree, or add the feed URL to feeds.conf.

include $(TOPDIR)/rules.mk

PKG_NAME:=vpn-watchdog
PKG_VERSION:=0.1.0
PKG_RELEASE:=1

PKG_SOURCE_PROTO:=git
PKG_SOURCE_URL:=https://github.com/frlvmxm-droid/darkroute.git
PKG_SOURCE_VERSION:=HEAD
PKG_SOURCE_SUBDIR:=$(PKG_NAME)-$(PKG_VERSION)
PKG_MIRROR_HASH:=skip

PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
PKG_USE_MIPS16:=0

GO_PKG:=github.com/frlvmxm-droid/darkroute/daemon
GO_PKG_BUILD_PKG:=$(GO_PKG)/cmd/vpn-watchdog
GO_PKG_LDFLAGS_X:=main.version=$(PKG_VERSION)

include $(INCLUDE_DIR)/package.mk
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

define Package/vpn-watchdog
  SECTION:=net
  CATEGORY:=Network
  SUBMENU:=VPN
  TITLE:=VPN Watchdog — automatic VPN failover
  URL:=https://github.com/frlvmxm-droid/darkroute
  DEPENDS:=+wireguard-tools +curl +iputils-ping +kmod-wireguard +nftables +ip-full +iptables-nft +ipset +kmod-ipt-ipset +sing-box
  MAINTAINER:=DarkRoute Project
endef

define Package/vpn-watchdog/description
  vpn-watchdog is an OpenWrt daemon that monitors internet connectivity
  and automatically switches between WireGuard, AmneziaWG, and VLESS
  VPN configurations when the active tunnel becomes unreachable.

  Features:
    - 4-state machine: HEALTHY / DEGRADED / PROBING / SWITCHING
    - Parallel probing of all candidates with RTT-based scoring
    - Automatic rollback if new config fails verification
    - LuCI web interface (separate luci-app-vpn-watchdog package)
endef

define Package/vpn-watchdog/conffiles
/etc/config/vpn-watchdog
/etc/vpn-watchdog/
endef

define Package/vpn-watchdog/install
	$(INSTALL_DIR)  $(1)/usr/bin
	$(INSTALL_BIN)  $(GO_PKG_BUILD_BIN_DIR)/vpn-watchdog $(1)/usr/bin/

	$(INSTALL_DIR)  $(1)/etc/init.d
	$(INSTALL_BIN)  ./files/vpn-watchdog.init $(1)/etc/init.d/vpn-watchdog

	$(INSTALL_DIR)  $(1)/etc/config
	$(INSTALL_CONF) ./files/vpn-watchdog.config $(1)/etc/config/vpn-watchdog

	$(INSTALL_DIR)  $(1)/etc/vpn-watchdog/configs
	$(INSTALL_DATA) ./files/example-wg.json $(1)/etc/vpn-watchdog/configs/

	$(INSTALL_DIR)  $(1)/etc/uci-defaults
	$(INSTALL_BIN)  ./files/90-vpn-watchdog $(1)/etc/uci-defaults/
endef

$(eval $(call GoBinPackage,vpn-watchdog))
$(eval $(call BuildPackage,vpn-watchdog))
