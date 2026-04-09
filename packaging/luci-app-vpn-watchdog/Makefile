# OpenWrt package Makefile for luci-app-vpn-watchdog.

include $(TOPDIR)/rules.mk

PKG_NAME:=luci-app-vpn-watchdog
PKG_VERSION:=0.1.0
PKG_RELEASE:=1

PKG_BUILD_DIR:=$(BUILD_DIR)/$(PKG_NAME)

include $(INCLUDE_DIR)/package.mk

define Package/luci-app-vpn-watchdog
  SECTION:=luci
  CATEGORY:=LuCI
  SUBMENU:=3. Applications
  TITLE:=LuCI interface for vpn-watchdog
  URL:=https://github.com/frlvmxm-droid/darkroute
  DEPENDS:=+luci-base +vpn-watchdog
  MAINTAINER:=DarkRoute Project
endef

define Package/luci-app-vpn-watchdog/description
  LuCI web interface for the vpn-watchdog daemon.
  Provides Dashboard, Configuration editor (with import from WireGuard
  config blocks and VLESS URIs), Settings, and live Log viewer.
endef

define Package/luci-app-vpn-watchdog/conffiles
/etc/config/vpn-watchdog
endef

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
endef

define Build/Configure
endef

define Build/Compile
endef

define Package/luci-app-vpn-watchdog/install
	$(INSTALL_DIR) $(1)/usr/lib/lua/luci/controller
	$(INSTALL_DATA) $(CURDIR)/../../luci-app/luasrc/controller/vpn-watchdog.lua \
	                $(1)/usr/lib/lua/luci/controller/

	$(INSTALL_DIR) $(1)/usr/lib/lua/luci/model/cbi/vpn-watchdog
	$(INSTALL_DATA) $(CURDIR)/../../luci-app/luasrc/model/cbi/vpn-watchdog/settings.lua \
	                $(1)/usr/lib/lua/luci/model/cbi/vpn-watchdog/

	$(INSTALL_DIR) $(1)/usr/lib/lua/luci/view/vpn-watchdog
	$(INSTALL_DATA) $(CURDIR)/../../luci-app/luasrc/view/vpn-watchdog/*.htm \
	                $(1)/usr/lib/lua/luci/view/vpn-watchdog/

	$(INSTALL_DIR) $(1)/www/luci-static/resources/vpn-watchdog
	$(INSTALL_DATA) $(CURDIR)/../../luci-app/htdocs/luci-static/resources/vpn-watchdog/app.js \
	                $(1)/www/luci-static/resources/vpn-watchdog/

	$(INSTALL_DIR) $(1)/usr/share/luci/menu.d
	$(INSTALL_DATA) $(CURDIR)/../../luci-app/root/usr/share/luci/menu.d/vpn-watchdog.json \
	                $(1)/usr/share/luci/menu.d/

	$(INSTALL_DIR) $(1)/usr/share/rpcd/acl.d
	$(INSTALL_DATA) ./files/luci-app-vpn-watchdog.json \
	                $(1)/usr/share/rpcd/acl.d/
endef

$(eval $(call BuildPackage,luci-app-vpn-watchdog))
