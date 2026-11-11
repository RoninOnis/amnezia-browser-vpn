/* Тесты сторожа автостарта (ensureTunnel) и миграции настроек.
 *
 * Жалоба с Windows: «хелпер не стартует вместе с Firefox, помогает только повторное
 * нажатие "подключить"». Причина — autoConnect был выключен по умолчанию, а авто-лечение
 * внутри refreshStatus срабатывало только при включённом тумблере.
 *
 * Важно: background.js на загрузке запускает `const ready = loadSettings().then(...)`,
 * и этот промис перезаписывает state.settings значениями из storage. Поэтому каждый тест
 * сначала дожидается ready, и только потом настраивает состояние.
 *
 * Run: node --test extension/tests/autostart.test.mjs
 */
import assert from "node:assert/strict";
import { test } from "node:test";
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const extDir = path.resolve(here, "..");

// объекты из vm-контекста имеют чужие прототипы — сравниваем через JSON
const plain = (v) => JSON.parse(JSON.stringify(v));

function stubBrowser(storage = {}) {
  const noop = () => {};
  const promise = () => Promise.resolve();
  const store = JSON.parse(JSON.stringify(storage));
  return {
    proxy: { onRequest: { addListener: noop }, onError: { addListener: noop } },
    storage: {
      local: {
        get: async () => store,
        set: async (obj) => Object.assign(store, obj),
      },
    },
    action: { setBadgeText: promise, setBadgeBackgroundColor: promise, setTitle: promise },
    alarms: { create: promise, onAlarm: { addListener: noop } },
    tabs: { onRemoved: { addListener: noop }, query: async () => [] },
    runtime: {
      lastError: null,
      onMessage: { addListener: noop },
      onStartup: { addListener: noop },
      onInstalled: { addListener: noop },
      sendMessage: async () => {},
      getPlatformInfo: async () => ({ os: "win" }),
      connectNative: () => {
        throw new Error("no native host in tests");
      },
    },
  };
}

async function bootBackground(storage = {}) {
  const ctx = vm.createContext({
    browser: stubBrowser(storage),
    console,
    setTimeout,
    clearTimeout,
    setInterval,
    clearInterval,
    atob,
    btoa,
    Blob,
    Response,
    TextDecoder,
    URL,
    DecompressionStream,
    Map,
    Promise,
    Date,
  });
  vm.runInContext(fs.readFileSync(path.join(extDir, "lib/vpnparse.js"), "utf8"), ctx, { filename: "vpnparse.js" });
  vm.runInContext(fs.readFileSync(path.join(extDir, "background.js"), "utf8"), ctx, { filename: "background.js" });
  await vm.runInContext("ready", ctx); // дождаться, пока loadSettings перезапишет state
  return ctx;
}

const ev = (ctx, expr) => vm.runInContext(expr, ctx);

const PROFILE = `state.settings = { ...state.settings, enabled: true, autoConnect: true,
  profiles: [{ id: "p1", name: "t", endpoint: "203.0.113.10:40000", profile: {} }],
  activeProfileId: "p1" };`;

test("автостарт включён по умолчанию", async () => {
  const ctx = await bootBackground();
  assert.equal(ev(ctx, "DEFAULTS.autoConnect"), true);
  assert.equal(ev(ctx, "state.settings.autoConnect"), true);
});

test("старые настройки мигрируют: автостарт включается один раз и версия проставляется", async () => {
  const ctx = await bootBackground({ settings: { enabled: true, autoConnect: false, whitelist: [], blacklist: [] } });
  assert.equal(ev(ctx, "state.settings.autoConnect"), true);
  assert.equal(ev(ctx, "state.settings.settingsVersion"), 2);
});

test("настройки с актуальной версией не перезаписываются", async () => {
  const ctx = await bootBackground({ settings: { enabled: true, autoConnect: false, settingsVersion: 2 } });
  assert.equal(ev(ctx, "state.settings.autoConnect"), false, "явный выбор пользователя уважаем");
});

test("сторож не трогает туннель, когда всё выключено или уже работает", async () => {
  const ctx = await bootBackground();
  await ev(ctx, "state.settings = { ...state.settings, enabled: false };");
  assert.deepEqual(plain(await ev(ctx, "ensureTunnel()")), { skipped: "off" });

  await ev(ctx, PROFILE);
  await ev(ctx, 'state.tunnel = { running: true, state: "connected" };');
  assert.deepEqual(plain(await ev(ctx, "ensureTunnel()")), { skipped: "running" });
});

test("сторож поднимает туннель, если он включён, но не работает", async () => {
  const ctx = await bootBackground();
  await ev(ctx, PROFILE);
  await ev(ctx, 'state.tunnel = { running: false, state: "stopped" };');
  await ev(ctx, "doConnect = async () => { state.tunnel = { running: true, state: 'connected' }; return { ok: true }; };");
  const res = await ev(ctx, "ensureTunnel()");
  assert.equal(res.ok, true);
  assert.equal(ev(ctx, "state.nextRetryAt"), 0, "после успеха отсрочка снята");
});

test("при неудаче пауза растёт и упирается в потолок 5 минут", async () => {
  const ctx = await bootBackground();
  await ev(ctx, PROFILE);
  await ev(ctx, "doConnect = async () => { state.tunnel = { running: false, state: 'error', error: 'down' }; return { ok: false }; };");

  await ev(ctx, "ensureTunnel()");
  assert.equal(ev(ctx, "state.retryDelay"), 30000, "первая пауза удваивается");

  await ev(ctx, "state.nextRetryAt = 0;");
  await ev(ctx, "ensureTunnel()");
  assert.equal(ev(ctx, "state.retryDelay"), 60000);

  await ev(ctx, "state.nextRetryAt = 0; state.retryDelay = 240000;");
  await ev(ctx, "ensureTunnel()");
  assert.equal(ev(ctx, "state.retryDelay"), 300000, "потолок в пять минут");
});

test("во время отсрочки сторож не долбит сервер", async () => {
  const ctx = await bootBackground();
  await ev(ctx, PROFILE);
  await ev(ctx, `globalThis.__calls = 0;
    doConnect = async () => { globalThis.__calls++; state.tunnel = { running: false, state: "error" }; return { ok: false }; };`);
  await ev(ctx, "ensureTunnel()");
  const calls = ev(ctx, "globalThis.__calls");
  await ev(ctx, "ensureTunnel()"); // сразу же — должна сработать отсрочка
  assert.equal(ev(ctx, "globalThis.__calls"), calls, "повторного подключения быть не должно");
  assert.deepEqual(plain(await ev(ctx, "ensureTunnel()")), { skipped: "backoff" });
});

test("явное включение сбрасывает отсрочку сторожа", async () => {
  const ctx = await bootBackground();
  await ev(ctx, PROFILE);
  await ev(ctx, "state.nextRetryAt = Date.now() + 120000; state.retryDelay = 240000;");
  await ev(ctx, "doConnect = async () => ({ ok: true }); state.settings = { ...state.settings, enabled: false };");
  await ev(ctx, "setEnabled(true)");
  assert.equal(ev(ctx, "state.nextRetryAt"), 0);
  assert.equal(ev(ctx, "state.retryDelay"), 15000);
});
