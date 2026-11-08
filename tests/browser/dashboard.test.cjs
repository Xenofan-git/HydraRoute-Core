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
  profile_id: 'foreign', source_id: 'providerA', pick_mode: 'auto',
  nodes: [
    { name: 'Alpha', key: { source: 'providerA', name: 'Alpha', nth: 0 }, picked: false, excluded: false },
    { name: 'Beta', key: { source: 'providerA', name: 'Beta', nth: 0 }, picked: false, excluded: false },
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
      if (url.pathname === '/v1/profiles' && req.method === 'GET') return json(res, 200, { profiles });
      if (url.pathname === '/v1/profiles' && req.method === 'PUT') {
        const update = await readBody(req);
        const profile = profiles.find(item => item.id === update.id);
        if (!profile) return json(res, 404, { error: 'profile not found' });
        Object.assign(profile, update);
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
      if (url.pathname === '/v1/refresh/status') return json(res, 200, { enabled: false, running: false, consecutive_failures: 0 });
      if (url.pathname === '/v1/profiles/preview' && req.method === 'POST') return json(res, 200, preview);
      if (url.pathname === '/v1/selections' && req.method === 'PUT') {
        savedSelection = await readBody(req);
        return json(res, 200, { status: 'saved', profile_id: savedSelection.id });
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


test('Russian menu opens on Rules and rules can be reordered by dragging', async () => {
  const page = await browser.newPage();
  try {
    await page.goto(baseURL);
    await page.getByText('API подключён').waitFor();
    assert.equal(await page.locator('.nav-item').allTextContents(), ['▦Правила','♧Сеть','◎DNS','☷Hydra','ⓘИнфо','⚙Настройки','⇥Выход']);
    assert.equal(await page.locator('#pageTitle').textContent(), 'Правила');
    assert.equal(await page.locator('.rule-card[data-rule]').count(), 1);
    assert.equal(await page.locator('.rule-drag-handle').count(), 1);
  } finally { await page.close(); }
});

test('server picker supports manual selection, exclusions and forgotten missing nodes', async () => {
  savedSelection = undefined;
  const page = await browser.newPage();
  try {
    await page.goto(baseURL);
    await page.getByText('API подключён').waitFor();
    await page.locator('.nav-item[data-page="hydra"]').click();
    await page.locator('[data-preview-servers="foreign"]').click();
    await page.getByRole('heading', { name: 'Профиль foreign' }).waitFor();
    assert.equal(await page.locator('.server-card').count(), 2);
    assert.equal(await page.locator('[data-drop-missing="picked:0"]').count(), 1);

    await page.locator('#pickMode').selectOption('manual');
    assert.equal(await page.locator('.pick-node:checked').count(), 2);
    await page.locator('.exclude-node').nth(0).check();
    assert.equal(await page.locator('.pick-node').nth(0).isChecked(), false);
    assert.equal(await page.locator('.pick-node').nth(1).isChecked(), true);
    await page.locator('[data-drop-missing="picked:0"]').click();
    assert.equal(await page.locator('[data-drop-missing]').count(), 0);

    await page.locator('#saveSelection').click();
    await page.getByText(/Выбор серверов сохранён/).waitFor();
    assert.equal(savedSelection.pick_mode, 'manual');
    assert.deepEqual(savedSelection.picked, [{ source: 'providerA', name: 'Beta', nth: 0 }]);
    assert.deepEqual(savedSelection.excluded, [{ source: 'providerA', name: 'Alpha', nth: 0 }]);
  } finally {
    await page.close();
  }
});

test('manual mode refuses an empty pool', async () => {
  const page = await browser.newPage();
  try {
    await page.goto(baseURL);
    await page.getByText('API подключён').waitFor();
    await page.locator('.nav-item[data-page="hydra"]').click();
    await page.locator('[data-preview-servers="foreign"]').click();
    await page.locator('#pickMode').selectOption('manual');
    await page.locator('[data-drop-missing="picked:0"]').click();
    await page.locator('.pick-node').nth(0).uncheck();
    await page.locator('.pick-node').nth(1).uncheck();
    savedSelection = undefined;
    await page.locator('#saveSelection').click();
    await page.getByText('Для ручного режима выберите хотя бы один сервер').waitFor();
    assert.equal(savedSelection, undefined);
  } finally {
    await page.close();
  }
});

test('HRNeo navigation order and draggable routing rules are preserved', async () => {
  savedRouting = undefined;
  const page = await browser.newPage();
  try {
    await page.goto(baseURL);
    await page.getByText('API подключён').waitFor();
    const nav = await page.locator('.nav-list .nav-item').allTextContents();
    assert.deepEqual(nav.map(x => x.trim()), ['Правила', 'Сеть', 'DNS', 'Hydra', 'Инфо', 'Настройки', 'Выход']);
    assert.equal(await page.locator('.nav-item[data-page="rules"]').getAttribute('class').then(v => v.includes('active')), true);
    assert.equal(await page.locator('.rule-card').count(), 2);
    await page.locator('[data-drag-rule="0"]').dragTo(page.locator('.rule-card').nth(1));
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
    await page.locator('[data-edit-rule="0"]').click();
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
