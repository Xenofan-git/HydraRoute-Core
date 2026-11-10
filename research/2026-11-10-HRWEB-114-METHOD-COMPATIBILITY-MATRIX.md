# HRweb 1.68.0 → HydraRoute Core: per-method compatibility matrix

Date: 2026-11-10

Reference: exact 114 regular methods from `2026-11-05-HRWEB-API-HANDLER-REGISTRY-FULL.md`; stream method `proxy.pingServers` is separately listed as item 115.

Status is deliberately conservative: an endpoint with a similar name is not treated as full compatibility unless request/response and behavior are verified.

| # | HRweb method | Disposition | Core endpoint / mapping | Remaining gap / boundary |
|---:|---|---|---|---|
| 1 | `config.get` | Частично | /v1/profiles, /v1/routing, /v1/dns, typed settings | Нет единого HRNeo/HRweb config DTO; модель Core разделена на независимые ресурсы и хранение. |
| 2 | `config.save` | Частично | /v1/profiles, /v1/routing, /v1/dns, typed settings | Нет единого HRNeo/HRweb config DTO; модель Core разделена на независимые ресурсы и хранение. |
| 3 | `policies.list` | Исключено — Keenetic | — | Список и привязка Keenetic policy. Не переносить в host routes/ip rule или управление роутером. |
| 4 | `policy.connections.get` | Исключено — Keenetic | — | Список и привязка Keenetic policy. Не переносить в host routes/ip rule или управление роутером. |
| 5 | `policy.connections.set` | Исключено — Keenetic | — | Список и привязка Keenetic policy. Не переносить в host routes/ip rule или управление роутером. |
| 6 | `interfaces.list` | Адаптировано | GET /v1/interfaces | Read-only список локальных Linux интерфейсов; не Keenetic RCI интерфейсы. |
| 7 | `interfaces.system` | Частично | GET /v1/interfaces | Нет полного Keenetic system/interface metadata и RCI state. |
| 8 | `hrneo.status` | Адаптировано | GET /v1/runtime, /healthz | Core runtime и API liveness; это не статус исходного HRNeo service. |
| 9 | `hrneo.toggle` | Адаптировано | POST /v1/runtime | Start/stop управляемого Core Xray. |
| 10 | `hrneo.config.get` | Частично | /v1/routing, /v1/l7/settings, /v1/dns, /v1/observatory/settings | Настройки типизированы и разделены; нет монолитного hrneo config_t и router-only полей. |
| 11 | `hrneo.config.save` | Частично | /v1/routing, /v1/l7/settings, /v1/dns, /v1/observatory/settings | Настройки типизированы и разделены; нет монолитного hrneo config_t и router-only полей. |
| 12 | `hrneo.log` | Частично | GET/DELETE /v1/events, GET /v1/events/stream | Структурированный журнал Core; raw HRNeo/Xray stdout/stderr не выдаётся. |
| 13 | `hrneo.log.clear` | Частично | GET/DELETE /v1/events, GET /v1/events/stream | Структурированный журнал Core; raw HRNeo/Xray stdout/stderr не выдаётся. |
| 14 | `rci.status` | Исключено — Keenetic RCI | — | RCI статус/токен не нужен независимому Debian Core; не хранить router credentials. |
| 15 | `hrneo.rciToken.get` | Исключено — Keenetic RCI | — | RCI статус/токен не нужен независимому Debian Core; не хранить router credentials. |
| 16 | `hrneo.rciToken.save` | Исключено — Keenetic RCI | — | RCI статус/токен не нужен независимому Debian Core; не хранить router credentials. |
| 17 | `hrweb.config.get` | Частично | Профили, routing и отдельные settings endpoints | Нет единого HRweb config object; применяются только явно поддержанные Core-настройки. |
| 18 | `hrweb.config.patch` | Частично | Профили, routing и отдельные settings endpoints | Нет единого HRweb config object; применяются только явно поддержанные Core-настройки. |
| 19 | `files.list` | Ожидает реализации | — | Нужен отдельный API с sandbox/workspace allowlist, защитой traversal/symlink и атомарными операциями. |
| 20 | `files.delete` | Ожидает реализации | — | Нужен отдельный API с sandbox/workspace allowlist, защитой traversal/symlink и атомарными операциями. |
| 21 | `files.rename` | Ожидает реализации | — | Нужен отдельный API с sandbox/workspace allowlist, защитой traversal/symlink и атомарными операциями. |
| 22 | `files.mkdir` | Ожидает реализации | — | Нужен отдельный API с sandbox/workspace allowlist, защитой traversal/symlink и атомарными операциями. |
| 23 | `geo.status` | Адаптировано | GET /v1/geodata/status | Проверка geoip.dat/geosite.dat, размера, hash и безопасного пути; не полный geo manager. |
| 24 | `geo.listSave` | Ожидает реализации / частично | Компилятор принимает валидированные GeoIP/GeoSite tags | Нет CRUD списков, preset manager, aggregator DB, каталога/тегов API и безопасного download/update/rollback workflow. |
| 25 | `geo.listToggle` | Ожидает реализации / частично | Компилятор принимает валидированные GeoIP/GeoSite tags | Нет CRUD списков, preset manager, aggregator DB, каталога/тегов API и безопасного download/update/rollback workflow. |
| 26 | `geo.listDelete` | Ожидает реализации / частично | Компилятор принимает валидированные GeoIP/GeoSite tags | Нет CRUD списков, preset manager, aggregator DB, каталога/тегов API и безопасного download/update/rollback workflow. |
| 27 | `geo.settingsSave` | Ожидает реализации / частично | Компилятор принимает валидированные GeoIP/GeoSite tags | Нет CRUD списков, preset manager, aggregator DB, каталога/тегов API и безопасного download/update/rollback workflow. |
| 28 | `geo.presetSizes` | Ожидает реализации / частично | Компилятор принимает валидированные GeoIP/GeoSite tags | Нет CRUD списков, preset manager, aggregator DB, каталога/тегов API и безопасного download/update/rollback workflow. |
| 29 | `geo.reset` | Ожидает реализации / частично | Компилятор принимает валидированные GeoIP/GeoSite tags | Нет CRUD списков, preset manager, aggregator DB, каталога/тегов API и безопасного download/update/rollback workflow. |
| 30 | `geo.aggregatorDb` | Ожидает реализации / частично | Компилятор принимает валидированные GeoIP/GeoSite tags | Нет CRUD списков, preset manager, aggregator DB, каталога/тегов API и безопасного download/update/rollback workflow. |
| 31 | `geo.catalog` | Ожидает реализации / частично | Компилятор принимает валидированные GeoIP/GeoSite tags | Нет CRUD списков, preset manager, aggregator DB, каталога/тегов API и безопасного download/update/rollback workflow. |
| 32 | `geo.readiness` | Адаптировано | GET /v1/geodata/status | Проверка geoip.dat/geosite.dat, размера, hash и безопасного пути; не полный geo manager. |
| 33 | `geo.tags` | Ожидает реализации / частично | Компилятор принимает валидированные GeoIP/GeoSite tags | Нет CRUD списков, preset manager, aggregator DB, каталога/тегов API и безопасного download/update/rollback workflow. |
| 34 | `dns.checkAdd` | Ожидает реализации | — | Нет отдельного списка доменов для check/monitor. |
| 35 | `dns.checkRemove` | Ожидает реализации | — | Нет отдельного списка доменов для check/monitor. |
| 36 | `dns.servers` | Частично | GET/PUT /v1/dns (upstream array) | CRUD возможен через конфигурационный массив целиком; отдельных resource endpoints и точного HRNeo DTO нет. |
| 37 | `dns.serverSave` | Частично | GET/PUT /v1/dns (upstream array) | CRUD возможен через конфигурационный массив целиком; отдельных resource endpoints и точного HRNeo DTO нет. |
| 38 | `dns.serverDelete` | Частично | GET/PUT /v1/dns (upstream array) | CRUD возможен через конфигурационный массив целиком; отдельных resource endpoints и точного HRNeo DTO нет. |
| 39 | `dns.serverCheck` | Частично | POST /v1/dns/check | Проверка домена через выбранные Core upstream; нет полного per-server probe/latency workflow. |
| 40 | `dns.resolve` | Частично | POST /v1/dns/check | Проверка домена через выбранные Core upstream; нет полного per-server probe/latency workflow. |
| 41 | `dns.get` | Адаптировано | GET /v1/dns | Возвращает конфигурацию, состояние службы и статистику Core. |
| 42 | `dns.status` | Адаптировано | GET /v1/dns | Возвращает конфигурацию, состояние службы и статистику Core. |
| 43 | `dns.names` | Ожидает реализации | — | Нет отдельного локального DNS names inventory endpoint. |
| 44 | `dns.enable` | Адаптировано | POST /v1/dns/service | Опциональный loopback resolver lifecycle; без system DNS/resolv.conf mutation. |
| 45 | `dns.disable` | Адаптировано | POST /v1/dns/service | Опциональный loopback resolver lifecycle; без system DNS/resolv.conf mutation. |
| 46 | `dns.settingsSave` | Частично | PUT /v1/dns | Настройки, правила и hosts передаются как единый typed config; точная операция/семантика не 1:1. |
| 47 | `dns.statsSave` | Частично | PUT /v1/dns | Настройки, правила и hosts передаются как единый typed config; точная операция/семантика не 1:1. |
| 48 | `dns.statsReset` | Ожидает реализации | — | Нет DNS sets/groups CRUD, per-set statistics и reset API. |
| 49 | `dns.setSave` | Ожидает реализации | — | Нет DNS sets/groups CRUD, per-set statistics и reset API. |
| 50 | `dns.setStats` | Ожидает реализации | — | Нет DNS sets/groups CRUD, per-set statistics и reset API. |
| 51 | `dns.ruleToggle` | Частично | PUT /v1/dns | Настройки, правила и hosts передаются как единый typed config; точная операция/семантика не 1:1. |
| 52 | `dns.groupGet` | Ожидает реализации | — | Нет DNS sets/groups CRUD, per-set statistics и reset API. |
| 53 | `dns.groupSave` | Ожидает реализации | — | Нет DNS sets/groups CRUD, per-set statistics и reset API. |
| 54 | `dns.groupToggle` | Ожидает реализации | — | Нет DNS sets/groups CRUD, per-set statistics и reset API. |
| 55 | `dns.groupDelete` | Ожидает реализации | — | Нет DNS sets/groups CRUD, per-set statistics и reset API. |
| 56 | `dns.blockSave` | Частично / ожидает реализации | PUT /v1/dns: Rules, Blocklists, BlockingEnabled | Custom domain rules реально применяются; URL blocklist хранится как конфигурация, загрузка/парсинг/обновление/restore не реализованы. |
| 57 | `dns.blockToggle` | Частично / ожидает реализации | PUT /v1/dns: Rules, Blocklists, BlockingEnabled | Custom domain rules реально применяются; URL blocklist хранится как конфигурация, загрузка/парсинг/обновление/restore не реализованы. |
| 58 | `dns.blockCheck` | Частично / ожидает реализации | PUT /v1/dns: Rules, Blocklists, BlockingEnabled | Custom domain rules реально применяются; URL blocklist хранится как конфигурация, загрузка/парсинг/обновление/restore не реализованы. |
| 59 | `dns.blockRemove` | Частично / ожидает реализации | PUT /v1/dns: Rules, Blocklists, BlockingEnabled | Custom domain rules реально применяются; URL blocklist хранится как конфигурация, загрузка/парсинг/обновление/restore не реализованы. |
| 60 | `dns.blockRestore` | Частично / ожидает реализации | PUT /v1/dns: Rules, Blocklists, BlockingEnabled | Custom domain rules реально применяются; URL blocklist хранится как конфигурация, загрузка/парсинг/обновление/restore не реализованы. |
| 61 | `dns.blockListSave` | Частично / ожидает реализации | PUT /v1/dns: Rules, Blocklists, BlockingEnabled | Custom domain rules реально применяются; URL blocklist хранится как конфигурация, загрузка/парсинг/обновление/restore не реализованы. |
| 62 | `dns.blockListToggle` | Частично / ожидает реализации | PUT /v1/dns: Rules, Blocklists, BlockingEnabled | Custom domain rules реально применяются; URL blocklist хранится как конфигурация, загрузка/парсинг/обновление/restore не реализованы. |
| 63 | `dns.blockListDelete` | Частично / ожидает реализации | PUT /v1/dns: Rules, Blocklists, BlockingEnabled | Custom domain rules реально применяются; URL blocklist хранится как конфигурация, загрузка/парсинг/обновление/restore не реализованы. |
| 64 | `dns.blockUpdate` | Частично / ожидает реализации | PUT /v1/dns: Rules, Blocklists, BlockingEnabled | Custom domain rules реально применяются; URL blocklist хранится как конфигурация, загрузка/парсинг/обновление/restore не реализованы. |
| 65 | `dns.blockCustomGet` | Частично / ожидает реализации | PUT /v1/dns: Rules, Blocklists, BlockingEnabled | Custom domain rules реально применяются; URL blocklist хранится как конфигурация, загрузка/парсинг/обновление/restore не реализованы. |
| 66 | `dns.blockCustomSave` | Частично / ожидает реализации | PUT /v1/dns: Rules, Blocklists, BlockingEnabled | Custom domain rules реально применяются; URL blocklist хранится как конфигурация, загрузка/парсинг/обновление/restore не реализованы. |
| 67 | `dns.kn.get` | Исключено — Keenetic | — | Keenetic DNS settings/RCI; не меняем router DNS. |
| 68 | `dns.kn.save` | Исключено — Keenetic | — | Keenetic DNS settings/RCI; не меняем router DNS. |
| 69 | `dns.hosts.get` | Частично | PUT /v1/dns | Настройки, правила и hosts передаются как единый typed config; точная операция/семантика не 1:1. |
| 70 | `dns.hosts.save` | Частично | PUT /v1/dns | Настройки, правила и hosts передаются как единый typed config; точная операция/семантика не 1:1. |
| 71 | `health.status` | Адаптировано | GET /v1/health/monitor | Локальный Core Health Monitor с component checks и metrics. |
| 72 | `health.checkNow` | Адаптировано | POST /v1/health/check | Ручная проверка Core components. |
| 73 | `health.rebuildMap` | Ожидает реализации | — | Отдельная карта/топология здоровья не реализована. |
| 74 | `events.list` | Адаптировано | GET /v1/events; GET /v1/events/stream | Bounded journal, Last-Event-ID, SSE, private persistence; не raw process logs. |
| 75 | `system.metrics` | Частично | GET /v1/health/monitor | Диск/RAM/load/uptime; CPU history, network throughput and DNS history parity отсутствуют. |
| 76 | `system.ping` | Адаптировано | GET /healthz | Liveness endpoint, не полный HRweb ping semantics. |
| 77 | `system.info` | Частично | UI Info + /v1/runtime + health metrics | Базовая информация; полный system/kernel/package inventory отсутствует. |
| 78 | `system.konami` | Исключено — нецелевой Easter egg | — | Не влияет на функциональность proxy manager. |
| 79 | `system.updates.check` | Ожидает реализации | — | Нужен отдельный Debian package/update workflow с проверкой, подтверждением и rollback; не использовать opkg. |
| 80 | `system.updates.apply` | Ожидает реализации | — | Нужен отдельный Debian package/update workflow с проверкой, подтверждением и rollback; не использовать opkg. |
| 81 | `fqdn.groups` | Ожидает реализации | — | Нет FQDN group file manager/API; Xray domain rules не эквивалентны CRUD группам HRNeo. |
| 82 | `fqdn.read` | Ожидает реализации | — | Нет FQDN group file manager/API; Xray domain rules не эквивалентны CRUD группам HRNeo. |
| 83 | `fqdn.write` | Ожидает реализации | — | Нет FQDN group file manager/API; Xray domain rules не эквивалентны CRUD группам HRNeo. |
| 84 | `net.routerWeb` | Исключено — Keenetic/network adapter | — | Router web proxy/fetch side effects не относятся к независимому Core. |
| 85 | `net.fetchProxy` | Исключено — Keenetic/network adapter | — | Router web proxy/fetch side effects не относятся к независимому Core. |
| 86 | `proxy.check` | Частично | GET /v1/runtime | Нет отдельной installed/binary-discovery state и HRweb response contract. |
| 87 | `proxy.status` | Адаптировано | GET /v1/runtime | Показывает configured/running/PID/last exit; installed/enabled/ready разделяются не полностью. |
| 88 | `proxy.start` | Адаптировано | POST /v1/runtime | Управление только Core Xray process. |
| 89 | `proxy.stop` | Адаптировано | POST /v1/runtime | Управление только Core Xray process. |
| 90 | `proxy.install` | Ожидает реализации | — | Нужен Debian-native installer/capability checker, не Entware/opkg installer. |
| 91 | `proxy.checkRequirements` | Ожидает реализации | — | Нужен Debian-native installer/capability checker, не Entware/opkg installer. |
| 92 | `proxy.createInterface` | Адаптировано | POST/PUT/DELETE /v1/profiles | Core profile CRUD, independent SOCKS endpoints. |
| 93 | `proxy.deleteInterface` | Адаптировано | POST/PUT/DELETE /v1/profiles | Core profile CRUD, independent SOCKS endpoints. |
| 94 | `proxy.interfaces` | Адаптировано | GET /v1/profiles | Core profiles, не Keenetic interface registry. |
| 95 | `proxy.activeServers` | Частично | POST /v1/profiles/preview; GET /v1/observatory/status | Eligible/picked/outbound state; нет отдельного HRweb activeServers response DTO. |
| 96 | `proxy.fetchSubscription` | Адаптировано | POST /v1/subscriptions/compile; profile source fetch/preview | VMess/VLESS/Trojan/Shadowsocks parsing; exact HRweb fetch response/header behavior not byte-compatible. |
| 97 | `proxy.refreshSubscription` | Адаптировано | POST /v1/profiles/refresh | Core refresh scheduler and last-known-good cache; exact HRweb payload differs. |
| 98 | `proxy.interfaceServers` | Адаптировано | POST /v1/profiles/preview | Preview/picker includes source/name/nth keys, picked/excluded and filters. |
| 99 | `proxy.applyServerSelection` | Адаптировано | PUT /v1/profiles; POST /v1/selections | Picked vs excluded and auto/manual selection are modeled separately. |
| 100 | `proxy.pickServers` | Адаптировано | PUT /v1/profiles; POST /v1/selections | Picked vs excluded and auto/manual selection are modeled separately. |
| 101 | `proxy.serverPicker` | Адаптировано | POST /v1/profiles/preview | Preview/picker includes source/name/nth keys, picked/excluded and filters. |
| 102 | `proxy.toggleMux` | Ожидает реализации | — | No per-profile Mux control equivalent currently exposed; do not assume original ambiguous iface parameter. |
| 103 | `proxy.toggleOpt` | Ожидает точного сопоставления | DPI settings are separately exposed as per-profile dpi_bypass | Original opt names/semantics must be traced before mapping to DPI controls. |
| 104 | `proxy.setOutInterface` | Адаптировано | PUT /v1/profiles: outbound_interface | Per-profile Xray sockopt.interface; no global route or RCI commit. |
| 105 | `proxy.toggleXrayIface` | Исключено — Keenetic adapter boundary | — | Original handlers mutate RCI/router interface state; no implicit host route or interface changes. |
| 106 | `proxy.changeStrategy` | Адаптировано | PUT /v1/profiles: strategy | Per-profile leastPing/random/roundRobin mapping; exact tie/fallback semantics still require parity tests. |
| 107 | `proxy.createPhobos` | Ожидает реализации | — | Phobos parser, isolated process lifecycle, slot allocator, readiness, rollback and reconciliation pending. |
| 108 | `proxy.deletePhobos` | Ожидает реализации | — | Phobos parser, isolated process lifecycle, slot allocator, readiness, rollback and reconciliation pending. |
| 109 | `proxy.togglePhobos` | Ожидает реализации | — | Phobos parser, isolated process lifecycle, slot allocator, readiness, rollback and reconciliation pending. |
| 110 | `proxy.phobosObf` | Ожидает реализации | — | Phobos parser, isolated process lifecycle, slot allocator, readiness, rollback and reconciliation pending. |
| 111 | `proxy.toggleExtIface` | Исключено — Keenetic adapter boundary | — | Original handlers mutate RCI/router interface state; no implicit host route or interface changes. |
| 112 | `probe.run` | Частично | POST /v1/profiles/test; GET /v1/profiles/ping | Core has test and separate ICMP/proxy ping streams; exact probe kinds/subjects and results contract still need mapping. |
| 113 | `speed.catalog` | Ожидает реализации | — | No speed-test catalog/run/history workflow. |
| 114 | `speed.results` | Ожидает реализации | — | No speed-test catalog/run/history workflow. |

## Separate streaming registry

| HRweb stream | Core mapping | Status | Gap |
|---|---|---|---|
| `proxy.pingServers` | `GET /v1/profiles/ping` SSE | Адаптировано / частично | Keeps ICMP echo and Xray proxy ping separate, uses eligible picked nodes and bounded workers; event payload is Core-specific and requires final parity checks. |
| `events` / event delivery | `GET /v1/events/stream` SSE | Core-native | Persistent journal stream with Last-Event-ID, subscriber cap and heartbeat; original regular `events.list` remains mapped separately. |

## Count by disposition

- Адаптировано: 27
- Исключено — Keenetic: 5
- Исключено — Keenetic RCI: 3
- Исключено — Keenetic adapter boundary: 2
- Исключено — Keenetic/network adapter: 2
- Исключено — нецелевой Easter egg: 1
- Ожидает реализации: 29
- Ожидает реализации / частично: 9
- Ожидает точного сопоставления: 1
- Частично: 24
- Частично / ожидает реализации: 11

Total regular methods classified: **114 / 114**.

## Overall result

No full 1:1 HRweb API compatibility is claimed. The strongest parity is in profiles, subscription/pool selection, Xray runtime, per-profile SOCKS and Observatory. DNS, GeoData, health, events, metrics and system operations are partial or Core-native adaptations. Router/RCI methods are intentionally excluded. File management, Phobos, system update, FQDN groups, speed tests, full Geo list management and DNS blocklist lifecycle remain pending.

The target is safe behavioral parity for useful Debian workflows, not copying Keenetic RCI/NDM side effects or exposing global routes/firewall/DNS mutations.
