# HydraRoute Core — полная функциональная совместимость с оригинальным HydraRoute Neo

Дата: 2026-11-09
Статус: аудит и поэтапная реализация; это целевой объём работ, не заявление о готовности.

## Цель
Перенести весь функционал оригинального HydraRoute Neo, который имеет смысл в самостоятельной Debian/Ubuntu ARM64 реализации. Оригинал использовать как поведенческий эталон; не переносить Entware/IPK бинарники, install scripts, web assets или механизмы RCI/NDM напрямую.

## Архитектурные инварианты
- Один независимый Core/Xray runtime, отдельный от 3x-ui/Xray.
- Один профиль/интерфейс приложения = собственная подписка, пул, выбор узлов, стратегия, настройки Xray и loopback SOCKS5 endpoint.
- Отсутствие глобальных IP routes, default-route изменений, ip rule, firewall/NAT и DNS mutations.
- Никаких зависимостей от Keenetic RCI; router-specific функции реализуются через отдельный адаптер только по отдельному согласованию.
- Существующие HydraRoute Neo, CSQTT, Direct rules, Tailscale и Reolink сервисы не трогать.
- Функции, которые в оригинале управляют Keenetic (policy binding, router interfaces, RCI commits), переносить как независимую локальную модель и API, а не выполнять изменения на роутере.

## Функциональная матрица по восстановленному HRweb/HRNeo API

### A. Профили, подписки и балансировщик
- Профили/интерфейсы: создание, изменение, удаление, включение/выключение, состояние и проверка требований.
- Подписки: fetch, refresh, заголовки источника, last-known-good cache, автоматическое обновление, ошибки/история обновления.
- Серверы: полноценный picker, список outbounds, протокол/имя/source/дубликаты, Picked/Excluded, select-all/clear-all, offline exclusion, фильтры keep/exclude, missing-node reconciliation.
- Проверки: ICMP echo отдельно от proxy/Xray Observatory; сортировка, актуальность измерений, health state.
- Balancing: leastPing, random, roundRobin; fallbackTag и корректная изоляция пулов.
- Xray options: Mux, дополнительные опции, outbound interface, Xray interface controls — после определения точной семантики каждой настройки.
- Интерфейсы приложения: локальная регистрация, собственные порты, SOCKS endpoint, статус и readiness.

### B. Proxy engine lifecycle
- Установка/обнаружение бинарника, версия и requirements check.
- Start/stop/restart, boot enablement, process status, config validation.
- Безопасное применение конфигурации: candidate validation, атомарная замена, rollback, last-known-good.
- Раздельные статусы installed/enabled/running/ready/data-plane healthy.

### C. Phobos / obfuscation / WireGuard
- Phobos URI parser и строгая валидация INI.
- SOCKS5 obfuscator и WireGuard obfuscator — отдельные backend modules.
- Управление instance lifecycle, toggle, status, logs, create/delete.
- Локальные slot/port allocators с реальной socket reservation.
- Native WireGuard config и peer status/handshake/data-plane checks.
- Транзакционный journal/saga, rollback и startup reconciliation.
- Не переносить Keenetic RCI builder; использовать Debian systemd и локальный registry.

### D. DNS / resolver (опциональный самостоятельный модуль)
- DNS servers CRUD/check/resolve.
- DNS sets/groups/rules, stats, enable/disable/settings.
- Blocklists: add/check/remove/restore, custom lists, scheduled update.
- Hosts overrides и локальные DNS names.
- Lifecycle/readiness resolver отдельного сервиса.
- Не менять системный DNS/resolv.conf и не перехватывать глобальный трафик без отдельного решения.

### E. Geo / FQDN / routing data
- Geo lists CRUD/toggle/delete/settings/reset.
- Presets, catalog, tags, aggregator DB, readiness.
- FQDN groups/read/write.
- Импортировать данные в управляемые Core файлы; не редактировать системные маршруты.

### F. Health / observability / operations
- Health monitor: interval, timeout, fail/recover thresholds, manual check/rebuild.
- Events/logs: list, clear, structured event stream.
- System metrics/info/ping/update checks.
- Probe run и speed catalog/results.
- Operation journal, progress events, cancellation, retries, bounded timeouts.

### G. Web/API and local file management
- Config get/save/patch with schema validation and atomic persistence.
- Local API/UI, CSRF/session protections, typed DTOs, event stream.
- Files list/delete/rename/mkdir только внутри явно разрешённого Core workspace; path traversal protection.
- Dashboard tabs and dialogs matching original workflows, not just endpoint compatibility.

## Восстановленный оригинальный API inventory
Статическое исследование HRweb 1.68.0-1 содержит 114 regular API methods плюс отдельные streaming methods. Группы включают config/policies/interfaces/HRNeo/RCI/HRweb/files/geo/dns/health/events/system/FQDN/net/proxy/probe/speed. Не все функции портируемы буквально: RCI, NDM, Keenetic policy commit и router-specific interfaces должны получить локальные аналоги или быть вынесены в adapter boundary.

## Реализационные этапы и критерии закрытия
1. Feature parity inventory: для каждого original method/UI action зафиксировать request, response, state mutation, dependencies, errors, UI entry point и Debian analogue.
2. Core profiles/subscriptions/server picker: закрыть поведенческие тесты и browser tests; отдельно сравнить с оригиналом.
3. Xray/proxy lifecycle and advanced options: ARM64 integration tests, rollback and two-profile isolation.
4. Phobos + WireGuard modules: parser, allocator, lifecycle, readiness and fault-injection tests.
5. DNS/resolver module: отдельный процесс/пакет, тесты и opt-in lifecycle.
6. Geo/FQDN/filter data plane: локальная реализация без системного маршрутизирования.
7. Health/events/system/speed tooling and file management.
8. UI/API parity review: пройти все original UI tabs/dialogs/actions; проверить каждый endpoint и состояние.
9. Release gates: full Go tests, vet, formatting, JS syntax, Playwright, ARM64 build, Xray validation, integration tests; then branch/PR/CI. Deployment to NanoPi remains separate and requires explicit go-ahead.

## Текущие ограничения среды
На VPS осталось около 620 MiB свободного места на корневом разделе при последней проверке. До тяжёлых сборок/Playwright/кросс-компиляции требуется оценивать свободное место; не удалять research, архивы, backups или существующие артефакты ради освобождения места без отдельного согласования.

## Статус на момент создания
- Core уже имеет профили, подписки/парсеры, Xray compiler/runtime, shared SOCKS routing, DNS package, interface settings и базовый UI/API.
- Работа над полным subscription/server-picker parity продолжается; текущие изменения ещё не перенесены в GitHub и не развёрнуты на NanoPi.
- Остальные группы выше являются планом полного функционального паритета, а не уже реализованными функциями.
