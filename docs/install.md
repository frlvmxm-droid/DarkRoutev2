# Installation Guide

## Prerequisites

- OpenWrt 21.02 or newer (22.03+ recommended)
- Target: MIPS 24kc, ARM Cortex-A7/A53, x86_64
- Flash space: ~3 MB for daemon + LuCI app
- RAM: ~10 MB runtime (5 MB daemon + sing-box if used)
- Optional: `amnezia-wireguard` kmod for AmneziaWG support

---

## Method 1 — opkg feed (recommended)

### One-command installer (recommended quick path)

```sh
wget -O - https://raw.githubusercontent.com/frlvmxm-droid/darkroute/main/install.sh | sh
```

Optional flags:

```sh
INSTALL_VLESS=1 wget -O - https://raw.githubusercontent.com/frlvmxm-droid/darkroute/main/install.sh | sh
INSTALL_AWG=1 wget -O - https://raw.githubusercontent.com/frlvmxm-droid/darkroute/main/install.sh | sh
```

The script:
- adds feed line if missing,
- runs `opkg update`,
- installs `vpn-watchdog` + `luci-app-vpn-watchdog`,
- optionally installs `sing-box` / `kmod-amnezia-wireguard` + `awg-tools`,
- enables and starts the service.

```sh
# Add the custom feed
echo "src/gz vpn-watchdog https://github.com/frlvmxm-droid/darkroute/releases/latest/download" \
  >> /etc/opkg/customfeeds.conf

# Update package lists
opkg update

# Install
opkg install vpn-watchdog luci-app-vpn-watchdog

# For AmneziaWG support (if available for your target):
opkg install kmod-amnezia-wireguard awg-tools

# For VLESS support:
opkg install sing-box
```

---

## Method 2 — Manual .ipk install

Download the `.ipk` files from GitHub Releases for your architecture:

| Architecture  | OpenWrt target                    |
|---------------|-----------------------------------|
| `mipsle`      | mipsel_24kc (e.g., GL.iNet AR150) |
| `mips`        | mips_24kc (e.g., TP-Link WR841N)  |
| `arm-cortex-a7`| arm_cortex-a7 (e.g., GL-MT300N)  |
| `aarch64`     | aarch64 (e.g., GL-MT3000, RPi4)   |
| `x86_64`      | x86_64 (PC/VM)                    |

```sh
# Upload .ipk files to the router, then:
opkg install --force-depends vpn-watchdog_0.1.0_mips_24kc.ipk
opkg install --force-depends luci-app-vpn-watchdog_0.1.0_all.ipk
```

---

## Method 3 — Build from source

```sh
# Clone the OpenWrt SDK for your target (example: 23.05.3 x86_64)
wget https://downloads.openwrt.org/releases/23.05.3/targets/x86/64/openwrt-sdk-23.05.3-x86-64_gcc-12.3.0_musl.Linux-x86_64.tar.xz
tar -xJf openwrt-sdk-*.tar.xz
cd openwrt-sdk-*

# Add this feed
echo "src-git vpn-watchdog https://github.com/frlvmxm-droid/darkroute.git;main" \
  >> feeds.conf.default
./scripts/feeds update vpn-watchdog packages
./scripts/feeds install -a -p vpn-watchdog
./scripts/feeds install golang

# Build
make package/vpn-watchdog/compile V=s
make package/luci-app-vpn-watchdog/compile V=s

# .ipk files in bin/packages/*/vpn-watchdog/
```

---

## Method 4 — Cudy TR3000 v1 (mediatek/filogic, OpenWrt 24.10.x)

Для вашего устройства (ARMv8 / Filogic) обычно используется архитектура пакетов
вроде `aarch64_cortex-a53`. Проверяйте точное значение:

```sh
opkg print-architecture
ubus call system board
```

### Вариант A — установить готовые пакеты, если есть в Releases

```sh
echo "src/gz vpn-watchdog https://github.com/frlvmxm-droid/darkroute/releases/latest/download" \
  >> /etc/opkg/customfeeds.conf
opkg update
opkg install vpn-watchdog luci-app-vpn-watchdog
```

Если используете VLESS / AWG:

```sh
opkg install sing-box
opkg install kmod-amnezia-wireguard awg-tools
```

### Вариант B — собрать .ipk под ваш target через OpenWrt SDK

1) Скачайте SDK именно под `releases/24.10.5/targets/mediatek/filogic/`  
   (файл вида `openwrt-sdk-24.10.5-mediatek-filogic_*.tar.xz`).

2) Сборка:

```sh
tar -xJf openwrt-sdk-24.10.5-mediatek-filogic_*.tar.xz
cd openwrt-sdk-24.10.5-mediatek-filogic_*

echo "src-git vpn-watchdog https://github.com/frlvmxm-droid/darkroute.git;main" \
  >> feeds.conf.default
./scripts/feeds update vpn-watchdog packages
./scripts/feeds install -a -p vpn-watchdog
./scripts/feeds install golang

make package/vpn-watchdog/compile V=s
make package/luci-app-vpn-watchdog/compile V=s
```

3) Результат:
- `bin/packages/<ARCH>/vpn-watchdog/*.ipk`
- установить на роутер через `opkg install ./<file>.ipk`

### Проверка после установки

```sh
/etc/init.d/vpn-watchdog enable
/etc/init.d/vpn-watchdog start
curl -s http://127.0.0.1:8765/health
```

Если health = `ok`, служба стартовала корректно.

### Детальный путь установки именно для вашей системы (TR3000 v1)

Ниже полный сценарий: **проверка роутера → сборка на Linux-хосте → установка на роутер**.

#### Шаг 0. Проверить роутер (в SSH на OpenWrt)

```sh
opkg print-architecture
ubus call system board
uname -a
```

Ожидаемо для вашего кейса:
- target: `mediatek/filogic`
- CPU: ARMv8 / aarch64
- OpenWrt: 24.10.x

#### Шаг 1. Подготовить Linux-хост для сборки

Пример для Debian/Ubuntu:

```sh
sudo apt update
sudo apt install -y git curl build-essential gawk unzip rsync python3 xz-utils file
```

#### Шаг 2. Запустить автосборку

```sh
git clone https://github.com/frlvmxm-droid/darkroute.git
cd darkroute
./scripts/build_tr3000_filogic.sh 24.10.5
```

После завершения скрипт выведет каталог с `.ipk`.

#### Шаг 3. Найти собранные пакеты на хосте

```sh
cd openwrt-sdk-24.10.5-mediatek-filogic_*/bin/packages/*/vpn-watchdog
ls -1 *.ipk
```

Как правило нужны:
- `vpn-watchdog_*.ipk`
- `luci-app-vpn-watchdog_*.ipk`

#### Шаг 4. Скопировать пакеты на роутер

```sh
scp vpn-watchdog_*.ipk luci-app-vpn-watchdog_*.ipk root@192.168.1.1:/tmp/
```

#### Шаг 5. Установить на роутере

```sh
ssh root@192.168.1.1
opkg update
opkg install /tmp/vpn-watchdog_*.ipk /tmp/luci-app-vpn-watchdog_*.ipk
```

Если нужен VLESS:

```sh
opkg install sing-box
```

Если нужен AWG:

```sh
opkg install kmod-amnezia-wireguard awg-tools
```

#### Шаг 6. Запустить и проверить

```sh
/etc/init.d/vpn-watchdog enable
/etc/init.d/vpn-watchdog start
/etc/init.d/vpn-watchdog status
curl -s http://127.0.0.1:8765/health
```

Должно вернуть `ok`.

#### Шаг 7. Проверить LuCI

- Откройте **LuCI → VPN Watchdog → Configurations**
- импортируйте:
  - `vless://...` или subscription URL (bulk),
  - WG/AWG `.conf` файлом (автозаполнение полей).
- Перейдите в **Dashboard** и убедитесь, что состояние `HEALTHY`.

---

## Initial Configuration

### 1. Add your first VPN configuration

**Via LuCI:** Navigate to *VPN Watchdog → Configurations → Add Configuration*.

Paste a WireGuard config block or VLESS URI into the import box and click *Parse*,
or fill in the form manually.

**Via SSH (WireGuard example):**

```sh
cat > /etc/vpn-watchdog/configs/my-wg.json << 'EOF'
{
  "id": "my-wg",
  "name": "My WireGuard Server",
  "protocol": "wg",
  "enabled": true,
  "interface_name": "vpn0",
  "routing_table_id": 100,
  "mtu": 1420,
  "wg": {
    "private_key": "YOUR_CLIENT_PRIVATE_KEY=",
    "public_key":  "SERVER_PUBLIC_KEY=",
    "preshared_key": "",
    "endpoint": "vpn.example.com:51820",
    "allowed_ips": ["0.0.0.0/0", "::/0"],
    "persistent_keepalive": 25
  }
}
EOF
chmod 600 /etc/vpn-watchdog/configs/my-wg.json
```

**AmneziaWG example:**

```sh
cat > /etc/vpn-watchdog/configs/my-awg.json << 'EOF'
{
  "id": "my-awg",
  "name": "My AmneziaWG",
  "protocol": "awg",
  "enabled": true,
  "interface_name": "vpn1",
  "routing_table_id": 101,
  "awg": {
    "private_key": "YOUR_PRIVATE_KEY=",
    "public_key":  "PEER_PUBLIC_KEY=",
    "endpoint": "vpn.example.com:51820",
    "allowed_ips": ["0.0.0.0/0"],
    "junk_packet_count": 4,
    "junk_packet_min_size": 40,
    "junk_packet_max_size": 70,
    "init_packet_junk_size": 0,
    "response_packet_junk_size": 0,
    "init_packet_magic_header": 1,
    "response_packet_magic_header": 2,
    "under_load_packet_magic_header": 3,
    "transport_packet_magic_header": 4
  }
}
EOF
chmod 600 /etc/vpn-watchdog/configs/my-awg.json
```

**VLESS example (or import from URI):**

```sh
# Import from a vless:// URI
echo 'vless://uuid@server:443?security=reality&flow=xtls-rprx-vision&pbk=KEY&sid=SHORTID&sni=sni.example.com&fp=chrome#MyServer' \
  | vpn-watchdog-import  # (tool not included; use LuCI import instead)

# Or write directly:
cat > /etc/vpn-watchdog/configs/my-vless.json << 'EOF'
{
  "id": "my-vless",
  "name": "My VLESS Reality",
  "protocol": "vless",
  "enabled": true,
  "interface_name": "vpn2",
  "routing_table_id": 102,
  "vless": {
    "uuid": "YOUR-UUID-HERE",
    "address": "server.example.com",
    "port": 443,
    "security": "reality",
    "flow": "xtls-rprx-vision",
    "transport": "tcp",
    "fingerprint": "chrome",
    "sni": "sni.example.com",
    "reality_public_key": "PUBLIC_KEY",
    "reality_short_id": "SHORTID",
    "local_port": 10801
  }
}
EOF
chmod 600 /etc/vpn-watchdog/configs/my-vless.json
```

### 2. Customise probe targets (optional)

Edit `/etc/config/vpn-watchdog` or use *VPN Watchdog → Settings* in LuCI:

```
config probe_target
    option host 'my-custom-target.example.com'
    option port '443'
    option type 'https'
```

### 3. Start the service

```sh
/etc/init.d/vpn-watchdog start
# Check status
/etc/init.d/vpn-watchdog status
# View logs
logread -e vpn-watchdog
```

---

## Checking Status

```sh
# Live status via HTTP API
curl -s http://127.0.0.1:8765/status | jq .

# State file (persisted across ticks)
cat /tmp/vpn-watchdog/state.json

# Scoring history
cat /tmp/vpn-watchdog/scores.json | jq .

# Logs
logread -e vpn-watchdog -l 50
```

---

## Troubleshooting

| Symptom | Check |
|---------|-------|
| Daemon doesn't start | `logread -e vpn-watchdog`; check `/etc/config/vpn-watchdog` enabled=1 |
| Always in PROBING | Probe targets unreachable; check firewall, DNS |
| WireGuard tunnel fails | Verify private/public key format (base64, 44 chars) |
| AWG tunnel fails | Ensure `kmod-amnezia-wireguard` and `awg-tools` are installed |
| VLESS tunnel fails | Check `sing-box` is installed; check sing-box logs in syslog |
| High CPU usage | Reduce number of configs; increase probe intervals in settings |

---

## Upgrading

```sh
opkg update
opkg upgrade vpn-watchdog luci-app-vpn-watchdog
# Your configs in /etc/vpn-watchdog/configs/ are preserved
```

To preserve custom configs during sysupgrade, add to `/etc/sysupgrade.conf`:

```
/etc/vpn-watchdog/configs/
/etc/config/vpn-watchdog
```
