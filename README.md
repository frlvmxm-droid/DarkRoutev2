# vpn-watchdog

Демон автоматического переключения VPN для OpenWrt. Следит за связностью через WireGuard, AmneziaWG и VLESS, автоматически переключается на лучший доступный туннель и обходит DPI-блокировки без участия пользователя.

---

## Содержание

- [Как это работает](#как-это-работает)
- [Быстрый FAQ: совместимость, ресурсы и настройка](#быстрый-faq-совместимость-ресурсы-и-настройка)
- [Установка](#установка)
- [Первоначальная настройка](#первоначальная-настройка)
- [Добавление VPN-конфигураций](#добавление-vpn-конфигураций)
- [Параметры UCI](#параметры-uci)
- [Инструменты и интерфейсы](#инструменты-и-интерфейсы)
- [HTTP API](#http-api)
- [DPI-обход и автонастройка](#dpi-обход-и-автонастройка)
- [Диагностика и логи](#диагностика-и-логи)
- [Устранение неполадок](#устранение-неполадок)
- [Сборка из исходников](#сборка-из-исходников)
- [Структура репозитория](#структура-репозитория)

---

## Как это работает

Демон работает по 4-шаговой машине состояний:

```
HEALTHY ──(3 ошибки)──▶ DEGRADED ──(3 ещё)──▶ PROBING ──▶ SWITCHING ──▶ HEALTHY
   ◀──────────────────────(любой успех)──────────────────────────(проверено)──┘
```

| Состояние  | Интервал пробинга | Переход                                            |
|------------|-------------------|----------------------------------------------------|
| HEALTHY    | 30 с              | ≥3 последовательных ошибок → DEGRADED              |
| DEGRADED   | 10 с              | ≥3 ещё → PROBING; любой успех → HEALTHY            |
| PROBING    | параллельно       | Выбирается лучший конфиг → SWITCHING               |
| SWITCHING  | —                 | Проверка OK → HEALTHY; ошибка → PROBING (откат)    |

В режиме **PROBING** все конфигурации тестируются параллельно через изолированные таблицы маршрутизации (SO_MARK / fwmark) — без прерывания текущего соединения. Лучший конфиг выбирается по мультикритериальной оценке: доступность обхода ТСПУ/DPI + производительность (RTT/потери) + защищенность + надежность сессионной истории.

---

## Быстрый FAQ: совместимость, ресурсы и настройка

### 1) Какие роутеры подходят (на базе каких процессоров)?

Подходят роутеры/шлюзы с OpenWrt для архитектур, под которые публикуются пакеты:

- `mips_24kc` / `mipsel_24kc`
- `arm_cortex-a7`
- `aarch64`
- `x86_64`

На практике лучше выбирать устройства уровня **ARMv7/AArch64/x86_64**, если планируется:
- VLESS через `sing-box`,
- агрессивный DPI auto-tune,
- подключение внешнего LLM Advisor.

### 2) Сколько памяти нужно?

Зависит от сценария:

- **Минимум для базового WG/AWG-сценария**: ~`128 MB RAM` и ~`32 MB flash`.
- **Рекомендуется для стабильной работы с VLESS + LuCI**: от `256 MB RAM` и от `64 MB flash`.
- **Если включён LLM Advisor + много вариантов DPI**: желательно `512 MB RAM+` (или x86_64/мощный ARM).

> Важный момент: основной расход памяти обычно даёт не сам `vpn-watchdog`, а туннельные рантаймы (`sing-box`, `wg/awg`) и параллельные проверки.

### 3) Как установить?

Короткий путь (рекомендуется):

```sh
echo "src/gz vpn-watchdog https://github.com/frlvmxm-droid/darkroute/releases/latest/download" \
  >> /etc/opkg/customfeeds.conf
opkg update
opkg install vpn-watchdog luci-app-vpn-watchdog
```

Далее по протоколам:

```sh
# AmneziaWG (опционально)
opkg install kmod-amnezia-wireguard awg-tools

# VLESS (опционально)
opkg install sing-box
```

После установки:

```sh
/etc/init.d/vpn-watchdog enable
/etc/init.d/vpn-watchdog start
```

### 4) Как в LuCI настроить подключение (WG/AWG/VLESS)?

1. Откройте **LuCI → Services → VPN Watchdog → Configurations**.  
2. Нажмите **Add** (или **Import**) и выберите протокол:
   - `wg` для WireGuard,
   - `awg` для AmneziaWG,
   - `vless` для VLESS/sing-box.
3. Заполните обязательные поля:
   - `id`, `name`, `interface_name`, `routing_table_id`,
   - endpoint/ключи для WG/AWG или UUID/адрес/порт для VLESS.
4. Убедитесь, что включено `enabled` и при необходимости `dpi.auto_tune`.
5. Сохраните конфиг и перейдите в **LuCI → Services → VPN Watchdog → Dashboard**:
   - проверьте `state=HEALTHY`,
   - проверьте `active_config_id`,
   - убедитесь, что растёт история score/attempts.

Дополнительно в **LuCI → Services → VPN Watchdog → Settings**:
- задайте probe targets (лучше 2–4 цели),
- включите DPI auto-tune и выберите профиль (`balanced` или `aggressive`),
- при необходимости добавьте `vpn_domain`/`vpn_ip` для policy-routing.

### 5) Как добавить и включить LLM для анализа обхода DPI?

На текущий момент основной способ — через UCI/SSH (это надёжнее и полностью поддерживается):

```sh
uci set vpn-watchdog.global.ai_enabled='1'
uci set vpn-watchdog.global.ai_provider='http_json'
uci set vpn-watchdog.global.ai_endpoint='http://127.0.0.1:8080/recommend'
uci set vpn-watchdog.global.ai_timeout='8'
uci set vpn-watchdog.global.ai_max_calls_per_hour='12'
uci set vpn-watchdog.global.ai_min_confidence='0.65'
uci set vpn-watchdog.global.ai_preset_ttl='43200'
uci commit vpn-watchdog
/etc/init.d/vpn-watchdog restart
```

Проверка:

```sh
curl -s http://127.0.0.1:8765/ai | jq .
```

Что важно по безопасности:
- LLM возвращает **только рекомендации** (без выполнения shell-команд),
- применяются ограничения allowlist полей,
- используется verify + cooldown/rollback.

---

## Установка

### Быстрая установка одной командой (как в podkop)

```sh
wget -O - https://raw.githubusercontent.com/frlvmxm-droid/darkroute/main/install.sh | sh
```

> Важно: для `raw.githubusercontent.com` **не** используется `/blob/` в URL.  
> Правильный шаблон: `https://raw.githubusercontent.com/<owner>/<repo>/<branch>/install.sh`

Опции:

```sh
# с поддержкой VLESS (sing-box)
INSTALL_VLESS=1 wget -O - https://raw.githubusercontent.com/frlvmxm-droid/darkroute/main/install.sh | sh

# с поддержкой AmneziaWG
INSTALL_AWG=1 wget -O - https://raw.githubusercontent.com/frlvmxm-droid/darkroute/main/install.sh | sh
```

Если вы форкнули/перенесли репозиторий, задайте feed явно:

```sh
FEED_URL='https://github.com/<owner>/<repo>/releases/latest/download' \
wget -O - https://raw.githubusercontent.com/<owner>/<repo>/main/install.sh | sh
```

Или через переменную `REPO` (она автоматически сформирует `FEED_URL`):

```sh
REPO='<owner>/<repo>' \
wget -O - https://raw.githubusercontent.com/<owner>/<repo>/main/install.sh | sh
```

### Метод 1 — через opkg-фид (рекомендуется)

```sh
# Добавить фид
echo "src/gz vpn-watchdog https://github.com/frlvmxm-droid/darkroute/releases/latest/download" \
  >> /etc/opkg/customfeeds.conf

# Обновить и установить
opkg update
opkg install vpn-watchdog luci-app-vpn-watchdog

# Для поддержки AmneziaWG (если есть для вашего чипа):
opkg install kmod-amnezia-wireguard awg-tools

# Для поддержки VLESS:
opkg install sing-box
```

### Метод 2 — ручная установка .ipk

Скачайте `.ipk`-файлы со страницы Releases для вашей архитектуры:

| Архитектура    | Целевая платформа                        | Пример устройства         |
|----------------|------------------------------------------|---------------------------|
| `mips_24kc`    | MIPS 24kc big-endian                     | TP-Link WR841N            |
| `mipsel_24kc`  | MIPS 24kc little-endian                  | GL.iNet AR150             |
| `arm_cortex-a7`| ARM Cortex-A7                            | GL-MT300N-V2              |
| `aarch64`      | AArch64                                  | GL-MT3000, Raspberry Pi 4 |
| `x86_64`       | x86_64 (ПК / ВМ)                        | x86 роутер / Proxmox      |

```sh
# Загрузить .ipk на роутер и установить
opkg install --force-depends vpn-watchdog_*.ipk
opkg install --force-depends luci-app-vpn-watchdog_*.ipk
```

### Метод 3 — сборка через OpenWrt SDK

```sh
# Скачать SDK для вашей платформы (пример: 23.05.3 x86_64)
wget https://downloads.openwrt.org/releases/23.05.3/targets/x86/64/openwrt-sdk-23.05.3-x86-64_gcc-12.3.0_musl.Linux-x86_64.tar.xz
tar -xJf openwrt-sdk-*.tar.xz
cd openwrt-sdk-*

# Добавить фид
echo "src-git vpn-watchdog https://github.com/frlvmxm-droid/darkroute.git;main" \
  >> feeds.conf.default
./scripts/feeds update vpn-watchdog packages
./scripts/feeds install -a -p vpn-watchdog
./scripts/feeds install golang

# Собрать
make package/vpn-watchdog/compile V=s
make package/luci-app-vpn-watchdog/compile V=s
# .ipk окажутся в bin/packages/*/vpn-watchdog/
```

### Зависимости

| Пакет                | Нужен для                                   |
|----------------------|---------------------------------------------|
| `wireguard-tools`    | WireGuard (`wg-quick`)                      |
| `kmod-wireguard`     | Ядерный модуль WireGuard                    |
| `sing-box`           | VLESS-туннель                               |
| `kmod-amnezia-wireguard` | AmneziaWG (опционально)               |
| `awg-tools`          | AmneziaWG (`awg-quick`, опционально)        |
| `iptables`           | Маршрутизация для VLESS                     |
| `ip-full`            | Политика маршрутизации (`ip rule/route`)    |

---

## Первоначальная настройка

### 1. Включить и запустить службу

```sh
/etc/init.d/vpn-watchdog enable
/etc/init.d/vpn-watchdog start
```

### 2. Проверить, что служба запустилась

```sh
/etc/init.d/vpn-watchdog status
# или
curl -s http://127.0.0.1:8765/health
```

### 3. Открыть веб-интерфейс

Перейдите в **LuCI → Services → VPN Watchdog**.

---

## Добавление VPN-конфигураций

Конфигурации хранятся как JSON-файлы в `/etc/vpn-watchdog/configs/`.  
Их можно добавлять тремя способами:

- **Через LuCI**: Services → VPN Watchdog → Configurations → Add  
  Поддерживается вставка WireGuard-блока или VLESS URI (кнопка *Import*)
- **Через SSH**: вручную создать JSON-файл (примеры ниже)
- **Через UCI**: `uci` для основных параметров службы

### WireGuard

```sh
cat > /etc/vpn-watchdog/configs/wg-main.json << 'EOF'
{
  "id": "wg-main",
  "name": "WireGuard Main",
  "protocol": "wg",
  "enabled": true,
  "interface_name": "vpn0",
  "routing_table_id": 100,
  "mtu": 1420,
  "wg": {
    "private_key": "YOUR_PRIVATE_KEY=",
    "public_key":  "SERVER_PUBLIC_KEY=",
    "preshared_key": "",
    "endpoint": "vpn.example.com:51820",
    "allowed_ips": ["0.0.0.0/0", "::/0"],
    "persistent_keepalive": 25,
    "dns": ["1.1.1.1", "1.0.0.1"]
  }
}
EOF
chmod 600 /etc/vpn-watchdog/configs/wg-main.json
```

### AmneziaWG (обфусцированный WireGuard)

```sh
cat > /etc/vpn-watchdog/configs/awg-backup.json << 'EOF'
{
  "id": "awg-backup",
  "name": "AmneziaWG Backup",
  "protocol": "awg",
  "enabled": true,
  "interface_name": "vpn1",
  "routing_table_id": 101,
  "awg": {
    "private_key": "YOUR_PRIVATE_KEY=",
    "public_key":  "PEER_PUBLIC_KEY=",
    "endpoint": "vpn.example.com:51820",
    "allowed_ips": ["0.0.0.0/0"],
    "persistent_keepalive": 25,
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
chmod 600 /etc/vpn-watchdog/configs/awg-backup.json
```

> **Параметры обфускации AmneziaWG** (`junk_packet_count`, `junk_packet_min_size` и др.) предоставляет сервер при регистрации.

### VLESS (via sing-box)

```sh
cat > /etc/vpn-watchdog/configs/vless-reality.json << 'EOF'
{
  "id": "vless-reality",
  "name": "VLESS Reality",
  "protocol": "vless",
  "enabled": true,
  "interface_name": "vpn2",
  "routing_table_id": 102,
  "vless": {
    "uuid": "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
    "address": "server.example.com",
    "port": 443,
    "security": "reality",
    "flow": "xtls-rprx-vision",
    "transport": "tcp",
    "fingerprint": "chrome",
    "sni": "www.microsoft.com",
    "reality_public_key": "PUBLIC_KEY_BASE64=",
    "reality_short_id": "abcdef12",
    "local_port": 10801
  }
}
EOF
chmod 600 /etc/vpn-watchdog/configs/vless-reality.json
```

> `local_port` — порт SOCKS5, на котором sing-box слушает локально (10800–10999). Каждый конфиг должен иметь уникальный порт.

**Поддерживаемые транспорты VLESS:**

| `transport` | `security`           | Описание                        |
|-------------|----------------------|---------------------------------|
| `tcp`       | `reality` / `tls`    | TCP с TLS/Reality               |
| `ws`        | `tls`                | WebSocket + TLS                 |
| `grpc`      | `tls`                | gRPC + TLS                      |
| `httpupgrade` | `tls`              | HTTP Upgrade + TLS              |

### Несколько конфигураций

Добавьте столько конфигураций, сколько нужно (разных протоколов, серверов). Демон автоматически выберет лучшую по результатам пробинга. Каждый конфиг должен иметь уникальный `id`, `interface_name` и `routing_table_id`.

---

## Параметры UCI

Главный конфигурационный файл: `/etc/config/vpn-watchdog`

```sh
# Просмотреть текущие настройки
uci show vpn-watchdog

# Изменить параметр (пример)
uci set vpn-watchdog.global.degraded_threshold='5'
uci commit vpn-watchdog
/etc/init.d/vpn-watchdog reload
```

### Основные параметры

| UCI-ключ                    | Умолч. | Описание                                               |
|-----------------------------|--------|--------------------------------------------------------|
| `global.enabled`            | `1`    | Включить/выключить службу                              |
| `global.log_level`          | `info` | Уровень логов: `debug`, `info`, `warn`, `error`        |
| `global.status_addr`        | `127.0.0.1:8765` | Адрес HTTP API статуса                    |
| `global.probe_interval_healthy` | `30` | Интервал пробинга в HEALTHY (секунды)               |
| `global.probe_interval_degraded` | `10` | Интервал пробинга в DEGRADED (секунды)             |
| `global.degraded_threshold` | `3`    | Последовательных ошибок до перехода в DEGRADED         |
| `global.probing_threshold`  | `3`    | Доп. ошибок до перехода в PROBING                      |
| `global.switch_verify_timeout` | `60` | Время на проверку нового конфига (секунды)           |
| `global.post_switch_cooldown` | `90` | Пауза после успешного переключения (секунды)          |
| `global.max_switch_attempts` | `3`  | Максимум попыток переключения за цикл PROBING          |
| `global.config_dir`         | `/etc/vpn-watchdog/configs` | Папка с JSON-конфигами        |
| `global.sing_box_bin`       | `/usr/bin/sing-box` | Путь к бинарнику sing-box               |
| `global.dpi_auto_tune`      | `1`    | Автогенерация DPI-вариантов (`0` — выключить)          |
| `global.dpi_max_variants`   | `8`    | Максимум DPI-вариантов на один базовый конфиг          |

### Цели пробинга

```
config probe_target
    option host '1.1.1.1'
    option type 'icmp'

config probe_target
    option host 'api.telegram.org'
    option port '443'
    option type 'https'
```

Добавить цель через SSH:
```sh
uci add vpn-watchdog probe_target
uci set vpn-watchdog.@probe_target[-1].host='8.8.8.8'
uci set vpn-watchdog.@probe_target[-1].type='icmp'
uci commit vpn-watchdog
```

**Типы пробинга:** `icmp` (TCP-fallback), `tcp`, `http`, `https`

Пробинг считается успешным, если >50% целей отвечают.

---

## Инструменты и интерфейсы

### LuCI (веб-интерфейс)

Доступен по адресу роутера в разделе **Services → VPN Watchdog**.

| Страница LuCI        | Описание                                               |
|----------------------|--------------------------------------------------------|
| **Dashboard**        | Текущее состояние (HEALTHY/DEGRADED/…), активный конфиг, DPI-статус, RTT/loss по каждому конфигу |
| **Configurations**   | Список конфигов, добавление/редактирование/удаление. Поддерживает импорт WireGuard-блока и VLESS URI |
| **Settings**         | Редактирование UCI-параметров без SSH                   |
| **Logs**             | Live-просмотр syslog-событий демона                    |

### init.d (служба)

```sh
/etc/init.d/vpn-watchdog start    # запустить
/etc/init.d/vpn-watchdog stop     # остановить
/etc/init.d/vpn-watchdog restart  # перезапустить
/etc/init.d/vpn-watchdog reload   # перечитать конфиг (SIGHUP, без разрыва туннеля)
/etc/init.d/vpn-watchdog status   # проверить, запущен ли демон
/etc/init.d/vpn-watchdog enable   # добавить в автозапуск
/etc/init.d/vpn-watchdog disable  # убрать из автозапуска
```

### Бинарник демона

```
vpn-watchdog [флаги]

Флаги:
  -log-level string    Уровень логов: debug|info|warn|error (по умолч.: info)
  -status-addr string  Адрес HTTP API (по умолч.: 127.0.0.1:8765; "" — выключить)
```

---

## HTTP API

Демон открывает локальный HTTP-сервер (по умолч. `127.0.0.1:8765`).  
Доступен только с роутера (не снаружи).

### GET /status

Полный JSON-снимок состояния:

```sh
curl -s http://127.0.0.1:8765/status | jq .
```

```json
{
  "state": "HEALTHY",
  "active_config_id": "wg-main",
  "consecutive_fails": 0,
  "last_switch": "2024-04-09T12:34:56Z",
  "switch_attempts": 0,
  "uptime_seconds": 3600,
  "dpi_block_type": "none",
  "dpi_evidence": [],
  "dpi_tested_at": "2024-04-09T12:00:00Z",
  "learned_count": 3,
  "scores": [
    {
      "config_id": "wg-main",
      "ewma_rtt_ms": 45.2,
      "ewma_loss": 0.01,
      "session_success_weight": 0.95,
      "dpi_bypass_success": 0.0
    }
  ]
}
```

### GET /dpi

Результат последнего DPI-анализа:

```sh
curl -s http://127.0.0.1:8765/dpi | jq .
```

```json
{
  "block_type": "tls",
  "evidence": [
    "TCP OK to 1.1.1.1:443",
    "HTTP OK to connectivitycheck.gstatic.com:80",
    "HTTPS FAIL: tls: handshake failure"
  ],
  "tested_at": "2024-04-09T11:58:00Z",
  "learned_variants_count": 5
}
```

**Типы блокировок (`block_type`):**

| Значение   | Значение                                              |
|------------|-------------------------------------------------------|
| `none`     | Блокировки не обнаружено                              |
| `tcp`      | Жёсткая блокировка на уровне TCP (RST / timeout)     |
| `http`     | HTTP-трафик режется DPI                               |
| `tls`      | TLS-рукопожатие блокируется (fingerprint-фильтр)     |
| `protocol` | Обнаружена сигнатура VPN-протокола                    |

### GET /health

Простая проверка живости (используется procd watchdog):

```sh
curl -s http://127.0.0.1:8765/health
# ok
```

---

## DPI-обход и автонастройка

Если обнаружена DPI-блокировка, демон автоматически генерирует варианты конфигурации и тестирует их в PROBING:

**WireGuard** — варианты MTU: 1200, 1280, 1360 байт.

**AmneziaWG** — три профиля обфускации:

| Профиль      | Jc | Jmin | Jmax | Когда применяется                  |
|--------------|----|------|------|------------------------------------|
| `mild`       | 2  | 20   | 50   | При любом типе блокировки          |
| `moderate`   | 4  | 40   | 70   | При HTTP/TLS-блокировке            |
| `aggressive` | 7  | 50   | 100  | При обнаружении VPN-сигнатуры      |

**VLESS** — матрица fingerprint × транспорт + path/service-name:

| Fingerprint                              | Транспорты                           | Path/service variants |
|------------------------------------------|--------------------------------------|-----------------------|
| `chrome`, `firefox`, `safari`, `ios`, `android`, `360` | `tcp`, `ws`, `grpc`, `httpupgrade` | `/`, `/cdn-cgi/trace`, `/api`, `/v1`, `grpc` |

Успешные варианты **запоминаются** и при следующем PROBING тестируются первыми.


### Что заимствовано из zapret / zapret2 / Podkop / GoodbyeDPI

Чтобы взять сильные стороны существующих anti-DPI проектов и закрыть их слабые места, в `vpn-watchdog` внедрены следующие практики:

| Источник | Сильная сторона | Как это применено в проекте |
|----------|------------------|-----------------------------|
| zapret / zapret2 | Профильный anti-DPI с несколькими техниками обхода и авто-подбором | Профили `compat/balanced/aggressive` + дифференциальная DPI-детекция (`tcp/http/tls/protocol`) и автоматический генератор вариантов. |
| zapret / zapret2 | Камуфляж и ротация параметров для обхода сигнатур | Добавлена ротация endpoint-портов (`443/80/53`) для WG/AWG и `port` для VLESS в авто-вариантах. |
| GoodbyeDPI | Практичный подход к обходу на L3/L4/L7 без ручного тюнинга для каждого сайта | Варианты строятся автоматически и ранжируются в PROBING через EWMA + история успешных сессий. |
| Podkop | Маршрутизация по доменам/IP и интеграция с sing-box для selective routing | Поддержка селекторов `vpn_domains`, `vpn_ips`, `vpn_domain_files` + VLESS/sing-box manager. |

Нюанс: в отличие от `zapret`/`GoodbyeDPI`, здесь фокус не на перехвате всего LAN-трафика через packet mangling, а на **устойчивом управлении VPN-туннелями и автопереключении**. Это снижает риск побочных эффектов на роутерах OpenWrt с малым CPU/RAM.

### Рекомендованные настройки для ТСПУ/DPI (RU)

```sh
# 1) Включить автотюнинг и увеличить пространство вариантов
uci set vpn-watchdog.global.dpi_auto_tune='1'
uci set vpn-watchdog.global.dpi_profile='aggressive'
uci set vpn-watchdog.global.dpi_max_variants='12'
uci commit vpn-watchdog

# 2) Перезапустить службу
/etc/init.d/vpn-watchdog restart
```

Практика для ТСПУ: сначала обычно срабатывает камуфляж порта (`443/80/53`) и web-like path для VLESS (`/cdn-cgi/trace`, `/api`), затем уже более «тяжёлые» изменения (обфускация AWG, смена fingerprint/transport).

### LLM Advisor (опционально)

Если стандартные варианты не дали результата, можно подключить внешний LLM через HTTP JSON API.
LLM получает:
- последний `dpi`-диагноз,
- историю попыток (`/tmp/vpn-watchdog/dpi_attempts.json`),
- список базовых конфигов.

LLM **не выполняет команды**: она возвращает только рекомендации из allowlist
(MTU, endpoint/port, VLESS transport/fingerprint/path, AWG profile). После этого демон делает verify, при успехе сохраняет временный AI-пресет в `/etc/vpn-watchdog/dpi_ai_presets.json`.

```sh
uci set vpn-watchdog.global.ai_enabled='1'
uci set vpn-watchdog.global.ai_provider='http_json'
uci set vpn-watchdog.global.ai_endpoint='http://127.0.0.1:8080/recommend'
uci set vpn-watchdog.global.ai_timeout='8'
uci set vpn-watchdog.global.ai_max_calls_per_hour='12'
uci set vpn-watchdog.global.ai_min_confidence='0.65'
uci set vpn-watchdog.global.ai_preset_ttl='43200'
uci commit vpn-watchdog
/etc/init.d/vpn-watchdog restart
```

Проверка статуса: `curl -s http://127.0.0.1:8765/ai | jq .`.

### Управление DPI-автонастройкой

```sh
# Выключить для всей службы
uci set vpn-watchdog.global.dpi_auto_tune='0'
uci commit vpn-watchdog

# Выключить для конкретного конфига (добавить в JSON)
# "dpi": { "auto_tune": false }

# Изменить максимальное число вариантов
uci set vpn-watchdog.global.dpi_max_variants='4'
uci commit vpn-watchdog
```

### Файлы DPI-данных

| Файл                                     | Описание                                        |
|------------------------------------------|-------------------------------------------------|
| `/tmp/vpn-watchdog/dpi_detection.json`   | Последний результат анализа (tmpfs, быстрый)    |
| `/etc/vpn-watchdog/dpi_learned.json`     | Эффективные варианты (persistent, сохр. после перезагрузки) |

---

## Диагностика и логи

```sh
# Текущий статус (машина состояний + активный конфиг)
curl -s http://127.0.0.1:8765/status | jq '{state, active_config_id, consecutive_fails}'

# DPI-статус
curl -s http://127.0.0.1:8765/dpi | jq .

# Реалтайм лог демона
logread -e vpn-watchdog -f

# Последние 50 строк лога
logread -e vpn-watchdog -l 50

# Файл состояния машины состояний (перезаписывается при переходах)
cat /tmp/vpn-watchdog/state.json

# История оценок конфигов (EWMA RTT, loss, session success)
cat /tmp/vpn-watchdog/scores.json | jq .

# Список запущенных sing-box процессов (для VLESS)
ps | grep sing-box

# Текущие правила маршрутизации (таблицы туннелей)
ip rule show
ip route show table 100   # таблица первого туннеля
```

---

## Устранение неполадок

| Симптом | Что проверить |
|---------|---------------|
| Демон не запускается | `logread -e vpn-watchdog`; проверить `enabled=1` в `/etc/config/vpn-watchdog` |
| Всегда в состоянии PROBING | Цели пробинга недоступны: проверить firewall, DNS, порты |
| WireGuard не поднимается | Проверить формат ключей (base64, 44 символа); наличие пакета `wireguard-tools` |
| AmneziaWG не поднимается | Установить `kmod-amnezia-wireguard` и `awg-tools`; проверить параметры обфускации |
| VLESS не поднимается | Проверить установку `sing-box`; лог: `logread -e sing-box` |
| DPI-варианты не помогают | Включить `log_level=debug`; проверить тип блока в `/dpi` API |
| Высокая нагрузка CPU | Уменьшить число конфигов; увеличить интервалы пробинга; уменьшить `dpi_max_variants` |
| Конфиги сбрасываются при обновлении прошивки | Добавить в `/etc/sysupgrade.conf` (см. ниже) |

### Сохранение конфигов при обновлении прошивки

```sh
cat >> /etc/sysupgrade.conf << 'EOF'
/etc/vpn-watchdog/configs/
/etc/config/vpn-watchdog
/etc/vpn-watchdog/dpi_learned.json
EOF
```

### Полная переустановка / сброс состояния

```sh
/etc/init.d/vpn-watchdog stop
rm -rf /tmp/vpn-watchdog/          # сбросить runtime-состояние
# конфиги в /etc/vpn-watchdog/configs/ сохраняются
/etc/init.d/vpn-watchdog start
```

### Обновление пакетов

```sh
opkg update
opkg upgrade vpn-watchdog luci-app-vpn-watchdog
# Конфиги в /etc/vpn-watchdog/configs/ сохраняются автоматически
```

---

## Сборка из исходников

Требования: Go 1.22+, Linux.

```sh
git clone https://github.com/frlvmxm-droid/darkroute.git
cd darkroute/daemon

# Собрать под текущую платформу
go build ./cmd/vpn-watchdog

# Запустить тесты
go test ./...

# Кросс-компиляция под MIPS (TP-Link и др.)
GOOS=linux GOARCH=mips GOMIPS=softfloat CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w" -o vpn-watchdog-mips ./cmd/vpn-watchdog

# Кросс-компиляция под ARM 64-bit (GL-MT3000, RPi4)
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w" -o vpn-watchdog-aarch64 ./cmd/vpn-watchdog

# Интеграционные тесты (требуют Docker)
cd ../tests
docker compose -f docker/docker-compose.yml up -d
./integration/test_state_machine.sh
```

---

## Структура репозитория

```
├── daemon/                     Go-демон
│   ├── cmd/vpn-watchdog/       main.go, HTTP API
│   └── internal/
│       ├── config/             Типы, UCI-загрузчик, JSON-стор
│       ├── probe/              Движок пробинга (ICMP/TCP/HTTP/HTTPS)
│       ├── scoring/            EWMA-база оценок
│       ├── state/              Машина состояний
│       ├── switch/             Движок переключения (параллельный пробинг)
│       ├── tunnel/             Менеджеры туннелей: WG, AWG, VLESS
│       ├── dpi/                DPI-детектор, генератор вариантов, персистентность
│       └── watchdog/           Главный event loop
├── luci-app/                   LuCI веб-интерфейс (Lua + HTML + JS)
├── packaging/
│   ├── vpn-watchdog/           OpenWrt Makefile + init + example-конфиги
│   └── luci-app-vpn-watchdog/  OpenWrt Makefile для LuCI
├── tests/
│   ├── docker/                 Docker Compose окружение для тестов
│   └── integration/            Интеграционные тест-скрипты
└── docs/
    ├── architecture.md         Детальная архитектура
    └── install.md              Расширенная инструкция по установке
```

---

## Лицензия

MIT
