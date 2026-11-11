/* Тесты релея на стороне расширения.
 *
 * Релей нужен там, где наружу пускают только UDP 443 (живой замер в корпоративной
 * сети: udp/443 OK, udp/37690 BLOCKED). Адрес и токен задаются в настройках, и
 * только при заполненном адресе они уходят в хелпер — иначе поле не влияет ни на что.
 *
 * Run: node --test extension/tests/relay.test.mjs
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

function stubBrowser(storage = {}) {
  const noop = () => {};
  const promise = () => Promise.resolve();
  const store = JSON.parse(JSON.stringify(storage));
  const sent = [];
  let messageHandler = null;

  const port = {
    postMessage(msg) {
      sent.push(msg);
      if (messageHandler) {
        setTimeout(() => messageHandler({
          id: msg.id,
          ok: true,
          status: { running: true, state: "connected", socksPort: 41234, mtu: 1280, mtuAuto: true, relay: true },
        }), 0);
      }
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

test("релей по умолчанию выключен и не мешает прямому подключению", async () => {
  const ctx = await bootBackground();
  assert.equal(ev(ctx, "DEFAULTS.relayAddr"), "");
  await ev(ctx, PROFILE);
  await ev(ctx, "connectOnce()");
  const connect = plain(ev(ctx, "browser.__sent")).find((m) => m.cmd === "connect");
  assert.equal(connect.relayAddr, undefined, "без адреса поле не отправляется");
  assert.equal(connect.relayToken, undefined);
});

test("адрес и токен релея уходят в хелпер, когда заданы", async () => {
  const ctx = await bootBackground();
  await ev(ctx, PROFILE);
  await ev(ctx, "state.settings = { ...state.settings, relayAddr: ' 213.165.34.238:443 ', relayToken: ' tok ' };");
  await ev(ctx, "connectOnce()");
  const connect = plain(ev(ctx, "browser.__sent")).filter((m) => m.cmd === "connect").pop();
  assert.equal(connect.relayAddr, "213.165.34.238:443", "пробелы должны обрезаться");
  assert.equal(connect.relayToken, "tok");
});

test("статус хелпера показывает работу через релей", async () => {
  const ctx = await bootBackground();
  const st = plain(await ev(ctx, "normaliseStatus({ running: true, state: 'connected', relay: true })"));
  assert.equal(st.relay, true);
  const noRelay = plain(await ev(ctx, "normaliseStatus({ running: true, state: 'connected' })"));
  assert.equal(noRelay.relay, false);
});

test("savePrefs сохраняет релей и не теряет MTU", async () => {
  const ctx = await bootBackground();
  await ev(ctx, "browser.__onMessage({ cmd: 'savePrefs', relayAddr: '1.2.3.4:443', relayToken: 'abc', mtuAuto: false, autoConnect: true, listenPort: 0 })");
  await new Promise((r) => setTimeout(r, 10));
  assert.equal(ev(ctx, "state.settings.relayAddr"), "1.2.3.4:443");
  assert.equal(ev(ctx, "state.settings.relayToken"), "abc");
  assert.equal(ev(ctx, "state.settings.mtuAuto"), false);
});

test("очистка адреса отключает релей", async () => {
  const ctx = await bootBackground({ settings: { relayAddr: "1.2.3.4:443", relayToken: "abc", settingsVersion: 2 } });
  await ev(ctx, "browser.__onMessage({ cmd: 'savePrefs', relayAddr: '', autoConnect: true, listenPort: 0 })");
  await new Promise((r) => setTimeout(r, 10));
  await ev(ctx, PROFILE);
  await ev(ctx, "connectOnce()");
  const connect = plain(ev(ctx, "browser.__sent")).filter((m) => m.cmd === "connect").pop();
  assert.equal(connect.relayAddr, undefined, "пустой адрес — релей не используется");
});
