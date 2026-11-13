const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
const { after, before, test } = require('node:test');
const { chromium } = require('playwright');

const root = path.resolve(__dirname, '../..');
const staticDir = path.join(root, 'internal/ui/static');
let server;
let browser;
let baseURL;
let savedSelection;
let savedRouting;
let savedRefresh;
let savedObservatorySettings = { probe_interval_seconds: 120 };
let savedL7Settings = { enabled: false, http: true, tls: true, quic: true };
let savedHealthSettings = { enabled: true, interval_seconds: 30, timeout_seconds: 4, fail_threshold: 2, recover_threshold: 2 };
let healthSnapshot = { status: 'healthy', checked_at: new Date().toISOString(), checks: [{ name: 'api', state: 'healthy', message: 'Local API is responding' }], metrics: {} };
let eventItems = [{ id: 1, time: new Date().toISOString(), level: 'info', component: 'api', type: 'started', message: 'HydraRoute Core API started' }];

const profiles = [{
  id: 'foreign', source_id: 'providerA', enabled: true,
  listen: '127.0.0.1', port: 16090, strategy: 'leastPing', dpi_bypass: {}, outbound_interface: '',
}, {
  id: 'torrent', source_id: 'providerB', enabled: true,
  listen: '127.0.0.1', port: 16091, strategy: 'random', dpi_bypass: {}, outbound_interface: '',
}];
const routing = {
  enabled: false, listen: '127.0.0.1', port: 16080,
  default_action: 'block', default_profile_id: '',
  rules: [{
    id: 'rule-1', name: 'Sample', enabled: true, match_type: 'domain',
    values: ['domain:old.example'], action: 'direct', profile_id: '',
  }, {
    id: 'rule-2', name: 'Second', enabled: true, match_type: 'domain',
    values: ['domain:second.example'], action: 'block', profile_id: '',
  }],
};
const preview = {
  profile_id: 'foreign', source_id: 'providerA', pick_mode: 'auto', auto_update_available: true, auto_update_enabled: false, auto_update_interval: 1440, filter_available: true, filter_keep: '', filter_exclude: '',
  nodes: [
    { name: 'Alpha', key: { source: 'providerA', name: 'Alpha', nth: 0 }, picked: true, excluded: false },
    { name: 'Beta', key: { source: 'providerA', name: 'Beta', nth: 0 }, picked: true, excluded: false },
  ],
  missing_picked: [{ source: 'providerA', name: 'Gone', nth: 0 }],
  missing_excluded: [],
};

function json(res, status, value) {
  res.writeHead(status, {
    'Content-Type': 'application/json; charset=utf-8',
    'Cache-Control': 'no-store',
  });
  res.end(JSON.stringify(value));
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    let body = '';
    req.setEncoding('utf8');
    req.on('data', chunk => { body += chunk; });
    req.on('end', () => {
      try { resolve(body ? JSON.parse(body) : {}); }
      catch (error) { reject(error); }
    });
    req.on('error', reject);
  });
}

before(async () => {
  server = http.createServer(async (req, res) => {
    const url = new URL(req.url, 'http://127.0.0.1');
    try {
      if (url.pathname === '/healthz') return json(res, 200, { status: 'ok' });
      if (url.pathname === '/v1/runtime') return json(res, 200, { running: false, configured: false });
      if (url.pathname === '/v1/health/monitor' && req.method === 'GET') return json(res, 200, healthSnapshot);
      if (url.pathname === '/v1/health/check' && req.method === 'POST') return json(res, 200, healthSnapshot);
      if (url.pathname === '/v1/health/settings' && req.method === 'GET') return json(res, 200, savedHealthSettings);
      if (url.pathname === '/v1/health/settings' && req.method === 'PUT') { savedHealthSettings = await readBody(req); return json(res, 200, savedHealthSettings); }
      if (url.pathname === '/v1/events' && req.method === 'GET') return json(res, 200, { events: eventItems.slice(-Number(url.searchParams.get('limit') || 50)) });
      if (url.pathname === '/v1/events' && req.method === 'DELETE') { eventItems = []; return json(res, 200, { status: 'cleared' }); }
      if (url.pathname === '/v1/events/stream' && req.method === 'GET') { res.writeHead(204); res.end(); return; }
      if (url.pathname === '/v1/profiles' && req.method === 'GET') return json(res, 200, { profiles });
      if (url.pathname === '/v1/profiles' && req.method === 'PUT') {
        const update = await readBody(req);
        const profile = profiles.find(item => item.id === update.id);
        if (!profile) return json(res, 404, { error: 'profile not found' });
        Object.assign(profile, update);
        savedSelection = update;
        return json(res, 200, { status: 'updated', profile_id: profile.id });
      }
      if (url.pathname === '/v1/interfaces' && req.method === 'GET') return json(res, 200, {
        default_interface: 'eth0', interfaces: [
          { name: 'wg0', category: 'VPN', up: true },
          { name: 'eth0', category: 'Интернет', up: true },
          { name: 'br0', category: 'Остальное', up: false },
        ],
      });
      if (url.pathname === '/v1/routing' && req.method === 'GET') return json(res, 200, { routing, configured: false });
      if (url.pathname === '/v1/refresh/status' && req.method === 'GET') return json(res, 200, savedRefresh || { enabled: false, interval_seconds: 86400, running: false, consecutive_failures: 0 });
      if (url.pathname === '/v1/refresh/status' && req.method === 'PUT') { savedRefresh = await readBody(req); return json(res, 200, { status: 'saved' }); }
      if (url.pathname === '/v1/profiles/preview' && req.method === 'POST') return json(res, 200, preview);
      if (url.pathname === '/v1/observatory/status' && req.method === 'GET') return json(res, 200, { available: false, statuses: {} });
      if (url.pathname === '/v1/observatory/settings' && req.method === 'GET') return json(res, 200, savedObservatorySettings);
      if (url.pathname === '/v1/observatory/settings' && req.method === 'PUT') { savedObservatorySettings = await readBody(req); return json(res, 200, { ...savedObservatorySettings, applies: 'prepared Xray configuration updated' }); }
      if (url.pathname === '/v1/geodata/status' && req.method === 'GET') return json(res, 200, { ready: true, assets: [{ name: 'geoip.dat', available: true, size: 3312784, sha256: 'a'.repeat(64) }, { name: 'geosite.dat', available: true, size: 7554929, sha256: 'b'.repeat(64) }] });
      if (url.pathname === '/v1/watchlist/status' && req.method === 'GET') return json(res, 200, { ready: true, path: '/var/lib/hydraroute/state/watchlist.sock', targets: 3, keys: 178, partial: true, excluded_patterns: 12 });
      if (url.pathname === '/v1/l7/settings' && req.method === 'GET') return json(res, 200, savedL7Settings);
      if (url.pathname === '/v1/l7/settings' && req.method === 'PUT') { savedL7Settings = await readBody(req); return json(res, 200, savedL7Settings); }
      if (url.pathname === '/v1/profiles/ping' && req.method === 'GET') {
        const keys = preview.nodes.map(n => JSON.stringify(n.key));
        const at = Math.floor(Date.now() / 1000) + 1;
        res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' });
        res.write('data: ' + JSON.stringify({ links: keys }) + '\n\n');
        res.write('data: ' + JSON.stringify({ link: keys[0], echo: { delay: 3 } }) + '\n\n');
        res.write('data: ' + JSON.stringify({ link: keys[1], echo: { delay: 5 } }) + '\n\n');
        res.write('data: ' + JSON.stringify({ link: keys[0], ping: { alive: false, delay: 0, at } }) + '\n\n');
        res.write('data: ' + JSON.stringify({ link: keys[1], ping: { alive: true, delay: 42, at } }) + '\n\n');
        res.end('data: ' + JSON.stringify({ done: true }) + '\n\n');
        return;
      }
      if (url.pathname === '/v1/routing' && req.method === 'PUT') {
        savedRouting = await readBody(req);
        Object.assign(routing, savedRouting);
        return json(res, 200, { status: 'saved' });
      }
      if (req.method !== 'GET') return json(res, 405, { error: 'method not allowed' });
      const asset = url.pathname === '/' ? 'index.html' : url.pathname.slice(1);
      if (!['index.html', 'app.js', 'app.css'].includes(asset)) return json(res, 404, { error: 'not found' });
      const body = fs.readFileSync(path.join(staticDir, asset));
      const type = asset.endsWith('.js') ? 'text/javascript; charset=utf-8'
        : asset.endsWith('.css') ? 'text/css; charset=utf-8' : 'text/html; charset=utf-8';
      res.writeHead(200, {
        'Content-Type': type,
        'Cache-Control': 'no-store',
        'X-Content-Type-Options': 'nosniff',
        'X-Frame-Options': 'DENY',
        'Referrer-Policy': 'no-referrer',
        'Content-Security-Policy': "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'",
      });
      res.end(body);
    } catch (error) {
      json(res, 500, { error: error.message });
    }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  baseURL = `http://127.0.0.1:${server.address().port}`;
  browser = await chromium.launch({ headless: true });
});

after(async () => {
  if (browser) await browser.close();
  if (server) await new Promise(resolve => server.close(resolve));
});


test('rules page displays local Watchlist readiness and partial coverage', async () => {
  const page = await browser.newPage();
  try {
    await page.goto(baseURL);
    await page.getByText('API подключён').waitFor();
    await page.getByRole('heading', { name: 'Watchlist MATCH / DUMP' }).waitFor();
    await page.getByText('Активен', { exact: true }).waitFor();
    await page.getByText(/Частичный список: 12/).waitFor();
  } finally {
    await page.close();
  }
});

test('rules page displays GeoIP and GeoSite asset readiness', async () => {
  const page = await browser.newPage();
  try {
    await page.goto(baseURL);
    await page.getByText('API подключён').waitFor();
    await page.getByRole('heading', { name: 'Геоданные Xray' }).waitFor();
    await page.getByText('GeoIP', { exact: true }).waitFor();
    await page.getByText('GeoSite', { exact: true }).waitFor();
    await page.getByText('Доступны', { exact: true }).waitFor();
  } finally {
    await page.close();
  }
});

test('Russian menu opens on Rules and rules can be reordered by dragging', async () => {
  const page = await browser.newPage();
  try {
    await page.goto(baseURL);
    await page.getByText('API подключён').waitFor();
    assert.deepEqual((await page.locator('.nav-item').allTextContents()).map(x => x.replace(/^[^\p{L}]+/u, '').trim()), ['Правила','Сеть','DNS','Hydra','Инфо','Настройки','Выход']);
    assert.equal(await page.locator('#pageTitle').textContent(), 'Правила');
    assert.equal(await page.locator('.rule-card[data-rule]').count(), 2);
    assert.equal(await page.locator('.rule-drag-handle').count(), 2);
  } finally { await page.close(); }
});

test('subscription modal supports select-all, manual node selection, filters and save', async () => {
  savedSelection = undefined;
  savedRefresh = undefined;
  const page = await browser.newPage();
  try {
    await page.goto(baseURL);
    await page.getByText('API подключён').waitFor();
    await page.locator('.nav-item[data-page="hydra"]').click();
    await page.locator('[data-preview-servers="foreign"]').click();
    await page.getByRole('heading', { name: 'Серверы: foreign' }).waitFor();
    assert.equal(await page.locator('.subscription-node-row').count(), 2);
    assert.equal(await page.locator('.pick-node:checked').count(), 2);
    await page.locator('#probeSubscription').click();
    await page.getByText('42 ms').waitFor();
    assert.match(await page.locator('.node-ping').nth(0).textContent(), /3 ms/);
    assert.match(await page.locator('.node-delay').nth(1).textContent(), /42 ms/);
    await page.locator('#excludeOffline').check();
    assert.equal(await page.locator('.pick-node').nth(0).isChecked(), false);
    assert.equal(await page.locator('.pick-node').nth(1).isChecked(), true);
    await page.locator('#subscriptionSearch').fill('Alpha');
    assert.equal(await page.locator('.subscription-node-row:visible').count(), 1);
    await page.locator('#subscriptionSearch').fill('');
    await page.locator('[data-sub-tab="filters"]').click();
    await page.locator('#filterInclude').fill('Beta');
    await page.locator('#applySubscriptionFilters').click();
    assert.equal(await page.locator('.subscription-node-row:visible').count(), 1);
    await page.locator('[data-drop-missing="picked:0"]').click();
    await page.locator('[data-sub-tab="update"]').click();
    await page.locator('#autoUpdateSubscription').check();
    await page.locator('#refreshHours').fill('24');
    await page.locator('#saveSubscription').click();
    await page.getByText('Настройки подписки сохранены и конфигурация применена').waitFor();
    assert.equal(savedSelection.pick_mode, 'manual');
    assert.deepEqual(savedSelection.picked, [{ source: 'providerA', name: 'Beta', nth: 0 }]);
    assert.deepEqual(savedSelection.excluded, [{ source: 'providerA', name: 'Alpha', nth: 0 }]);
    assert.equal(savedSelection.auto_update_enabled, true);
    assert.equal(savedSelection.auto_update_interval, 1440);
    assert.equal(savedSelection.filter_keep, 'Beta');
  } finally { await page.close(); }
});

test('subscription modal permits deliberately empty server pool', async () => {
  savedSelection = undefined;
  const page = await browser.newPage();
  try {
    await page.goto(baseURL);
    await page.getByText('API подключён').waitFor();
    await page.locator('.nav-item[data-page="hydra"]').click();
    await page.locator('[data-preview-servers="foreign"]').click();
    await page.locator('#pickAllNodes').uncheck();
    assert.equal(await page.locator('.pick-node:checked').count(), 0);
    await page.locator('#saveSubscription').click();
    await page.getByText('Настройки подписки сохранены').waitFor();
    assert.deepEqual(savedSelection.picked, []);
    assert.equal(savedSelection.pick_mode, 'manual');
  } finally { await page.close(); }
});

test('HRNeo navigation order and draggable routing rules are preserved', async () => {
  savedRouting = undefined;
  const page = await browser.newPage();
  try {
    await page.goto(baseURL);
    await page.getByText('API подключён').waitFor();
    const nav = await page.locator('.nav-list .nav-item').allTextContents();
    assert.deepEqual(nav.map(x => x.replace(/^[^\p{L}]+/u, '').trim()), ['Правила', 'Сеть', 'DNS', 'Hydra', 'Инфо', 'Настройки', 'Выход']);
    assert.equal(await page.locator('.nav-item[data-page="rules"]').getAttribute('class').then(v => v.includes('active')), true);
    assert.equal(await page.locator('.rule-card').count(), 2);
    await page.evaluate(() => {
      const source = document.querySelector('[data-drag-rule="0"]');
      const target = document.querySelector('.rule-card[data-rule="1"]');
      const dataTransfer = new DataTransfer();
      source.dispatchEvent(new DragEvent('dragstart', { bubbles: true, dataTransfer }));
      target.dispatchEvent(new DragEvent('dragover', { bubbles: true, cancelable: true, dataTransfer }));
      target.dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer }));
      source.dispatchEvent(new DragEvent('dragend', { bubbles: true, dataTransfer }));
    });
    assert.equal((await page.locator('.rule-card').first().locator('.rule-name-text').textContent()).trim(), 'Second');
    await page.locator('#saveRouting').click();
    await page.getByText('Настройки маршрутизации сохранены и применены').waitFor();
    assert.deepEqual(savedRouting.rules.map(rule => rule.name), ['Second', 'Sample']);
  } finally {
    await page.close();
  }
});

test('routing editor splits CRLF, LF and comma-separated values before saving', async () => {
  savedRouting = undefined;
  const page = await browser.newPage();
  try {
    await page.goto(baseURL);
    await page.getByText('API подключён').waitFor();
    await page.locator('.nav-item[data-page="rules"]').click();
    await page.locator('[data-edit-rule="0"]').dispatchEvent('click');
    await page.locator('.rule-values').fill('domain:one.example\r\ndomain:two.example,domain:three.example');
    await page.locator('#ruleEditorForm button[type="submit"]').click();
    await page.locator('#saveRouting').click();
    await page.getByText('Настройки маршрутизации сохранены и применены').waitFor();
    assert.deepEqual(savedRouting.rules[0].values, [
      'domain:one.example', 'domain:two.example', 'domain:three.example',
    ]);
  } finally {
    await page.close();
  }
});

test('L7 sniffing settings save the selected proxy-ingress protocols', async () => {
  const page = await browser.newPage();
  try {
    await page.goto(baseURL);
    await page.getByText('API подключён').waitFor();
    await page.locator('[data-page="settings"]').click();
    await page.getByRole('heading', { name: 'L7-анализ' }).waitFor();
    await page.locator('#l7Enabled').check();
    await page.locator('#l7HTTP').uncheck();
    await page.locator('#l7TLS').check();
    await page.locator('#l7QUIC').check();
    await page.locator('#saveL7Settings').click();
    await page.getByText('Настройки L7 сохранены').waitFor();
    assert.deepEqual(savedL7Settings, { enabled: true, http: false, tls: true, quic: true });
  } finally {
    await page.close();
  }
});

test('Observatory interval loads and persists through its settings API', async () => {
  const page = await browser.newPage();
  try {
    await page.goto(baseURL);
    await page.getByText('API подключён').waitFor();
    await page.locator('[data-page="settings"]').click();
    const interval = page.locator('#observatoryProbeInterval');
    await interval.waitFor();
    assert.equal(await interval.inputValue(), '120');
    await interval.fill('120');
    await page.locator('#saveObservatorySettings').click();
    await page.getByText('Интервал применён к конфигурации Xray').waitFor();
    assert.equal(savedObservatorySettings.probe_interval_seconds, 120);
  } finally {
    await page.close();
  }
});

test('connection settings persist independently per profile: DPI, balancing and outgoing interface', async () => {
  profiles.forEach(p => { p.strategy = p.id === 'torrent' ? 'random' : 'leastPing'; p.dpi_bypass = {}; p.outbound_interface = ''; });
  const page = await browser.newPage();
  try {
    await page.goto(baseURL);
    await page.getByText('API подключён').waitFor();
    await page.locator('.nav-item[data-page="network"]').click();
    const card = page.locator('.connection-card').filter({ hasText: 'foreign' });
    await card.locator('[data-connection-menu]').click();
    await card.locator('[data-menu-dpi]').click();
    await page.getByText('Шумовой трафик').waitFor();
    await page.locator('.connection-option-panel [name="noise_traffic"]').check();
    await page.locator('.connection-option-panel [name="random_tls_fingerprint"]').check();
    await page.locator('[data-option-save]').click();
    await page.getByText('Настройки подключения сохранены').waitFor();
    assert.deepEqual(profiles[0].dpi_bypass, { noise_traffic: true, tls_fragmentation: false, random_tls_fingerprint: true });
    assert.deepEqual(profiles[1].dpi_bypass, {});

    await card.locator('[data-connection-menu]').click();
    await card.locator('[data-menu-balance]').click();
    await page.locator('.connection-option-panel [value="roundRobin"]').check();
    await page.getByText('Настройки подключения сохранены').waitFor();
    assert.equal(profiles[0].strategy, 'roundRobin');
    assert.equal(profiles[1].strategy, 'random');

    await card.locator('[data-connection-menu]').click();
    await card.locator('[data-menu-interface]').click();
    await page.getByText('wg0').waitFor();
    await page.locator('[data-interface-value="wg0"]').click();
    await page.getByText('Настройки подключения сохранены').waitFor();
    assert.equal(profiles[0].outbound_interface, 'wg0');
    assert.equal(profiles[1].outbound_interface, '');
  } finally { await page.close(); }
});

test('Health Monitor settings persist and event journal is visible', async () => {
  const page = await browser.newPage();
  try {
    await page.goto(baseURL);
    await page.getByText('API подключён').waitFor();
    await page.locator('[data-page="settings"]').click();
    await page.getByRole('heading', { name: 'Health Monitor' }).first().waitFor();
    await page.locator('#healthInterval').fill('45');
    await page.locator('#healthTimeout').fill('3');
    await page.locator('#healthFailThreshold').fill('3');
    await page.locator('#healthRecoverThreshold').fill('2');
    await page.locator('#saveHealthSettings').click();
    await page.getByText('Настройки Health Monitor сохранены').waitFor();
    assert.deepEqual(savedHealthSettings, { enabled: true, interval_seconds: 45, timeout_seconds: 3, fail_threshold: 3, recover_threshold: 2 });
    await page.getByRole('heading', { name: 'Журнал событий' }).waitFor();
    await page.getByText('HydraRoute Core API started').waitFor();
  } finally {
    await page.close();
  }
});
