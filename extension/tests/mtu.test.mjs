/* Тесты подбора MTU.
 *
 * Зачем: у людей с корпоративным VPN (Cisco AnyConnect держит туннель с MTU 1400)
 * наши пакеты при MTU профиля 1376 молча теряются — handshake не приходит, а в
 * логе хелпера до 0.3.2 не было ни слова о том, что handshake не случился.
 *
 * Run: node --test extension/tests/mtu.test.mjs
 */
import assert from "node:assert/strict";
import { test } from "node:test";
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const extDir = path.resolve(here, "..");
const plain = (v) => JSON.parse(JSON.stringify(v));

// Заглушка native messaging: запоминаем, что расширение отправило хелперу,
// и отвечаем на всё успешным статусом с "подобранным" MTU.
function stubBrowser(storage = {}) {
  const noop = () => {};
  const promise = () => Promise.resolve();
  const store = JSON.parse(JSON.stringify(storage));
  const sent = [];
  let messageHandler = null;

  const port = {
    postMessage(msg) {
      sent.push(msg);
      const reply = {
        id: msg.id,
        ok: true,
        status: {
          running: true,
          state: "connected",
          socksPort: 41234,
          mtu: 1280,
          mtuAuto: true,
          rxBytes: 10,
          txBytes: 20,
        },
      };
      if (messageHandler) setTimeout(() => messageHandler(reply), 0);
    },
    onMessage: { addListener: (fn) => { messageHandler = fn; } },
    onDisconnect: { addListener: noop },
    disconnect: noop,
  };

  const ctxBrowser = {
    proxy: { onRequest: { addListener: noop }, onError: { addListener: noop } },
    storage: { local: { get: async () => store, set: async (o) => Object.assign(store, o) } },
    action: { setBadgeText: promise, setBadgeBackgroundColor: promise, setTitle: promise },
    alarms: { create: promise, onAlarm: { addListener: noop } },
    tabs: { onRemoved: { addListener: noop }, query: async () => [] },
    runtime: {
      lastError: null,
      onMessage: { addListener: (fn) => { ctxBrowser.__onMessage = fn; } },
      onStartup: { addListener: noop },
      onInstalled: { addListener: noop },
      sendMessage: async () => {},
      getPlatformInfo: async () => ({ os: "win" }),
      connectNative: () => port,
    },
    __sent: sent,
  };
  return ctxBrowser;
}

async function bootBackground(storage = {}) {
  const ctx = vm.createContext({
    browser: stubBrowser(storage),
    console, setTimeout, clearTimeout, setInterval, clearInterval,
    atob, btoa, Blob, Response, TextDecoder, URL, DecompressionStream, Map, Promise, Date,
  });
  vm.runInContext(fs.readFileSync(path.join(extDir, "lib/vpnparse.js"), "utf8"), ctx, { filename: "vpnparse.js" });
  vm.runInContext(fs.readFileSync(path.join(extDir, "background.js"), "utf8"), ctx, { filename: "background.js" });
  await vm.runInContext("ready", ctx);
  return ctx;
}

const ev = (ctx, expr) => vm.runInContext(expr, ctx);

const PROFILE = `state.settings = { ...state.settings, enabled: true, autoConnect: true,
  profiles: [{ id: "p1", name: "t", endpoint: "203.0.113.10:40000", profile: {} }],
  activeProfileId: "p1" };`;

test("подбор MTU включён по умолчанию, ручной MTU не задан", async () => {
  const ctx = await bootBackground();
  assert.equal(ev(ctx, "DEFAULTS.mtuAuto"), true);
  assert.equal(ev(ctx, "DEFAULTS.mtu"), 0);
  assert.equal(ev(ctx, "state.settings.mtuAuto"), true);
});

test("connect отправляет хелперу mtuAuto и пустой mtu", async () => {
  const ctx = await bootBackground();
  await ev(ctx, PROFILE);
  await ev(ctx, "connectOnce()");
  const sent = plain(ev(ctx, "browser.__sent"));
  const connect = sent.find((m) => m.cmd === "connect");
  assert.ok(connect, "сообщение connect не отправлено");
  assert.equal(connect.mtuAuto, true);
  assert.equal(connect.mtu, 0);
});

test("connect передаёт ручной MTU, если подбор выключен", async () => {
  const ctx = await bootBackground();
  await ev(ctx, PROFILE);
  await ev(ctx, "state.settings = { ...state.settings, mtuAuto: false, mtu: 1200 };");
  await ev(ctx, "connectOnce()");
  const connect = plain(ev(ctx, "browser.__sent")).filter((m) => m.cmd === "connect").pop();
  assert.equal(connect.mtuAuto, false);
  assert.equal(connect.mtu, 1200);
});

test("статус хелпера прокидывает MTU в попап", async () => {
  const ctx = await bootBackground();
  const st = plain(await ev(ctx, "normaliseStatus({ running: true, state: 'connected', mtu: 1280, mtuAuto: true })"));
  assert.equal(st.mtu, 1280);
  assert.equal(st.mtuAuto, true);

  const old = plain(await ev(ctx, "normaliseStatus({ running: true, state: 'connected' })"));
  assert.equal(old.mtu, 0, "старый хелпер без поля mtu не должен ломать попап");
  assert.equal(old.mtuAuto, false);
});

test("savePrefs сохраняет mtuAuto и не теряет остальные настройки", async () => {
  const ctx = await bootBackground();
  await ev(ctx, "browser.__onMessage({ cmd: 'savePrefs', mtuAuto: false, autoConnect: true, listenPort: 0 })");
  await new Promise((r) => setTimeout(r, 10));
  assert.equal(ev(ctx, "state.settings.mtuAuto"), false);
  assert.equal(ev(ctx, "state.settings.autoConnect"), true);
});
