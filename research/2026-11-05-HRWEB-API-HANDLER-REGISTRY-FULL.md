# HRweb 1.68.0 - complete regular API registry

Date: 2026-11-05

Method: static disassembly of main.init; registry map at 0x467860; runtime.mapassign_faststr call-site and string literal extraction; funcval code-pointer resolution. Binary was not executed.

## Result

- The regular API registry is initialized with map hint 114 at 0x2f0d40.
- Exactly 114 mapassign_faststr registrations target map 0x467860 (from 0x2f0d70 through 0x2f2c94).
- All 114 keys are unique; no duplicate key was found.
- Function-value code pointers were resolved for the static entries. proxy.start/proxy.stop are distinct captured method values using the same generic closure code; speed.results resolves to main.speedResults.
- Stream methods are in a separate registry and are not included here.
- proxy.installed is not a registry key: it is frontend StateManager state, not an API method.

## Complete regular registry

| API method | Handler / closure target | Code address |
|---|---|---:|
| `config.get` | `main.configGet` | `0x351d30` |
| `config.save` | `main.configSave` | `0x351fe0` |
| `policies.list` | `main.policiesList` | `0x3550d0` |
| `policy.connections.get` | `main.policyConnectionsGet` | `0x354900` |
| `policy.connections.set` | `main.policyConnectionsSet` | `0x354b80` |
| `interfaces.list` | `main.interfacesList` | `0x3545d0` |
| `interfaces.system` | `main.interfacesSystem` | `0x355360` |
| `hrneo.status` | `main.hrNeoStatus` | `0x3540a0` |
| `hrneo.toggle` | `main.hrNeoToggle` | `0x354250` |
| `hrneo.config.get` | `main.hrNeoConfigGet` | `0x3529e0` |
| `hrneo.config.save` | `main.hrNeoConfigSave` | `0x352ba0` |
| `hrneo.log` | `main.hrNeoLog` | `0x353540` |
| `hrneo.log.clear` | `main.hrNeoLogClear` | `0x353730` |
| `rci.status` | `main.rciStatus` | `0x353920` |
| `hrneo.rciToken.get` | `main.hrNeoRciTokenGet` | `0x353ba0` |
| `hrneo.rciToken.save` | `main.hrNeoRciTokenSave` | `0x353d10` |
| `hrweb.config.get` | `main.hrWebConfigGet` | `0x356120` |
| `hrweb.config.patch` | `main.hrWebConfigPatch` | `0x3562d0` |
| `files.list` | `main.filesList` | `0x341d30` |
| `files.delete` | `main.filesDelete` | `0x3422f0` |
| `files.rename` | `main.filesRename` | `0x342570` |
| `files.mkdir` | `main.filesMkdir` | `0x342960` |
| `geo.status` | `main.geoStatusHandler` | `0x34c680` |
| `geo.listSave` | `main.geoListSave` | `0x34c8f0` |
| `geo.listToggle` | `main.geoListToggle` | `0x34e3d0` |
| `geo.listDelete` | `main.geoListDelete` | `0x34eba0` |
| `geo.settingsSave` | `main.geoSettingsSave` | `0x34f390` |
| `geo.presetSizes` | `main.geoPresetSizes` | `0x34f7d0` |
| `geo.reset` | `main.geoReset` | `0x34f5a0` |
| `geo.aggregatorDb` | `main.geoAggregatorDB` | `0x3570b0` |
| `geo.catalog` | `main.geoCatalog` | `0x357320` |
| `geo.readiness` | `main.geoReadiness` | `0x34ff60` |
| `geo.tags` | `main.geoTags` | `0x347e90` |
| `dns.checkAdd` | `main.dnsCheckAdd` | `0x3554c0` |
| `dns.checkRemove` | `main.dnsCheckRemove` | `0x355970` |
| `dns.servers` | `main.dnsServersGet` | `0x318730` |
| `dns.serverSave` | `main.dnsServerSave` | `0x3189a0` |
| `dns.serverDelete` | `main.dnsServerDelete` | `0x318fa0` |
| `dns.serverCheck` | `main.dnsServerCheck` | `0x3195b0` |
| `dns.resolve` | `main.dnsResolve` | `0x319a40` |
| `dns.get` | `main.dnsGet` | `0x33bc20` |
| `dns.status` | `main.dnsStatus` | `0x339b90` |
| `dns.names` | `main.dnsNames` | `0x339c10` |
| `dns.enable` | `main.dnsEnable` | `0x33c080` |
| `dns.disable` | `main.dnsDisable` | `0x33c340` |
| `dns.settingsSave` | `main.dnsSettingsSave` | `0x33cc10` |
| `dns.statsSave` | `main.dnsStatsSave` | `0x33cfe0` |
| `dns.statsReset` | `main.dnsStatsReset` | `0x33d2c0` |
| `dns.setSave` | `main.dnsSetSave` | `0x33da90` |
| `dns.setStats` | `main.dnsSetStats` | `0x33dfe0` |
| `dns.ruleToggle` | `main.dnsRuleToggle` | `0x33e720` |
| `dns.groupGet` | `main.dnsGroupGet` | `0x33eff0` |
| `dns.groupSave` | `main.dnsGroupSave` | `0x33f5c0` |
| `dns.groupToggle` | `main.dnsGroupToggle` | `0x33fc90` |
| `dns.groupDelete` | `main.dnsGroupDelete` | `0x33ff50` |
| `dns.blockSave` | `main.dnsBlockSave` | `0x323df0` |
| `dns.blockToggle` | `main.dnsBlockToggle` | `0x324030` |
| `dns.blockCheck` | `main.dnsBlockCheckHandler` | `0x324280` |
| `dns.blockRemove` | `main.dnsBlockRemove` | `0x324410` |
| `dns.blockRestore` | `main.dnsBlockRestore` | `0x3249f0` |
| `dns.blockListSave` | `main.dnsBlockListSave` | `0x324c90` |
| `dns.blockListToggle` | `main.dnsBlockListToggle` | `0x3258f0` |
| `dns.blockListDelete` | `main.dnsBlockListDelete` | `0x325af0` |
| `dns.blockUpdate` | `main.dnsBlockUpdate` | `0x325dc0` |
| `dns.blockCustomGet` | `main.dnsBlockCustomGet` | `0x325f60` |
| `dns.blockCustomSave` | `main.dnsBlockCustomSave` | `0x3261f0` |
| `dns.kn.get` | `main.dnsKnGet` | `0x32d480` |
| `dns.kn.save` | `main.dnsKnSave` | `0x32d5b0` |
| `dns.hosts.get` | `main.dnsHostsGet` | `0x327f30` |
| `dns.hosts.save` | `main.dnsHostsSave` | `0x327fc0` |
| `health.status` | `main.healthStatus` | `0x356bd0` |
| `health.checkNow` | `main.healthCheckNow` | `0x356c80` |
| `health.rebuildMap` | `main.healthRebuildMap` | `0x356ce0` |
| `events.list` | `main.eventsList` | `0x341b00` |
| `system.metrics` | `main.systemMetrics` | `0x358740` |
| `system.ping` | `main.systemPing` | `0x358720` |
| `system.info` | `main.systemInfo` | `0x358f70` |
| `system.konami` | `main.konami` | `0x350be0` |
| `system.updates.check` | `main.systemUpdatesCheck` | `0x359810` |
| `system.updates.apply` | `main.systemUpdatesApply` | `0x359cf0` |
| `fqdn.groups` | `main.fqdnGroupsList` | `0x343c20` |
| `fqdn.read` | `main.fqdnRead` | `0x344190` |
| `fqdn.write` | `main.fqdnWrite` | `0x344730` |
| `net.routerWeb` | `main.routerWebMethod` | `0x355120` |
| `net.fetchProxy` | `main.netFetchProxy` | `0x356e40` |
| `proxy.check` | `main.proxyCheck` | `0x379550` |
| `proxy.status` | `main.proxyStatusMethod` | `0x3795e0` |
| `proxy.start` | `main.init.proxyOp.func82 -> hrweb/svc.(*Service).Enable-fm` | `0x2f2dd0` |
| `proxy.stop` | `main.init.proxyOp.func82 -> hrweb/svc.(*Service).Disable-fm` | `0x2f2dd0` |
| `proxy.install` | `main.proxyInstall` | `0x379660` |
| `proxy.checkRequirements` | `main.proxyCheckRequirements` | `0x379730` |
| `proxy.createInterface` | `main.proxyCreateInterface` | `0x379840` |
| `proxy.deleteInterface` | `main.proxyDeleteInterface` | `0x379e40` |
| `proxy.interfaces` | `main.proxyInterfacesMethod` | `0x37c260` |
| `proxy.activeServers` | `main.proxyActiveServers` | `0x37c1b0` |
| `proxy.fetchSubscription` | `main.proxyFetchSubscription` | `0x37c300` |
| `proxy.refreshSubscription` | `main.proxyRefreshSubscription` | `0x37c690` |
| `proxy.interfaceServers` | `main.proxyInterfaceServers` | `0x37c870` |
| `proxy.applyServerSelection` | `main.proxyApplySelection` | `0x37c9a0` |
| `proxy.pickServers` | `main.proxyPickServers` | `0x37ccc0` |
| `proxy.serverPicker` | `main.proxyServerPicker` | `0x37cee0` |
| `proxy.toggleMux` | `main.proxyToggleMux` | `0x37a3e0` |
| `proxy.toggleOpt` | `main.proxyToggleOpt` | `0x37a050` |
| `proxy.setOutInterface` | `main.proxySetOutInterface` | `0x37a200` |
| `proxy.toggleXrayIface` | `main.proxyToggleXrayIface` | `0x37a8b0` |
| `proxy.changeStrategy` | `main.proxyChangeStrategy` | `0x37a590` |
| `proxy.createPhobos` | `main.proxyCreatePhobos` | `0x3755b0` |
| `proxy.deletePhobos` | `main.proxyDeletePhobos` | `0x375790` |
| `proxy.togglePhobos` | `main.proxyTogglePhobos` | `0x375bc0` |
| `proxy.phobosObf` | `main.proxyPhobosObf` | `0x3759a0` |
| `proxy.toggleExtIface` | `main.proxyToggleExtIface` | `0x37b040` |
| `probe.run` | `main.probeRun` | `0x378a60` |
| `speed.catalog` | `main.speedCatalogGet` | `0x395980` |
| `speed.results` | `main.speedResults` | `0x395fd0` |

## Special closure entries

- `proxy.start` and `proxy.stop` both point to `main.init.proxyOp.func82` at 0x2f2dd0. Their captured method values differ: `hrweb/svc.(*Service).Enable-fm` for start and `hrweb/svc.(*Service).Disable-fm` for stop. This is why they do not have separate handler symbols.
- `speed.results` resolves through funcval object 0x6c08c8 to `main.speedResults` at 0x395fd0.

## Scope notes

- `proxy.installed` in the frontend is a state property used by proxy-manager UI; it is not among the 114 regular API map keys.
- `main.proxyInstalled` at 0x37cff0 exists as a function, but its route/registration is outside this regular method map and must be mapped separately if needed.
- `proxy.pingServers` is registered in the separate stream registry, not this regular map.
- This report resolves the full key-to-handler map for the regular API dispatcher. It does not imply every handler is reusable for Debian: RCI/NDM-specific methods still require the previously documented boundary classification.
