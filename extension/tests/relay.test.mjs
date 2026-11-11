/* Тесты поведения расширения при работе через 443 и «спутник сервера».
 *
 * Расклад: в сети с корпоративным VPN наружу пускают только UDP 443. На сервере
 * Amnezia можно поставить «спутник» — наш релей, который слушает 443 и передаёт
 * пакеты настоящему AWG. Тогда расширению НЕ нужно ничего знать про релей: оно
 * просто пробует 443 вторым шагом. Тесты фиксируют, что в payload нет лишних
 * полей и что статус корректно показывает подобранный MTU.
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
          status: { running: true, state: "connected", socksPort: 41234, mtu: 1280, mtuAuto: true },
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

test("в настройках больше нет полей релея — клиенту нечего вписывать", async () => {
  const ctx = await bootBackground();
  assert.equal(ev(ctx, "'relayAddr' in DEFAULTS"), false);
  assert.equal(ev(ctx, "'relayToken' in DEFAULTS"), false);
});

test("connect отправляет только профиль, listen, mtu и mtuAuto", async () => {
  const ctx = await bootBackground();
  await ev(ctx, PROFILE);
  await ev(ctx, "connectOnce()");
  const connect = plain(ev(ctx, "browser.__sent")).find((m) => m.cmd === "connect");
  assert.ok(connect, "connect не отправлен");
  const keys = Object.keys(connect).filter((k) => k !== "id" && k !== "cmd").sort();
  assert.deepEqual(keys, ["listen", "mtu", "mtuAuto", "profile"]);
  assert.equal(connect.mtuAuto, true, "443 и меньшие MTU пробуются сами, без настроек");
});

test("статус показывает подобранный MTU", async () => {
  const ctx = await bootBackground();
  const st = plain(await ev(ctx, "normaliseStatus({ running: true, state: 'connected', mtu: 1280, mtuAuto: true })"));
  assert.equal(st.mtu, 1280);
  assert.equal(st.mtuAuto, true);
});

test("savePrefs не воскрешает удалённые поля", async () => {
  const ctx = await bootBackground();
  await ev(ctx, "browser.__onMessage({ cmd: 'savePrefs', autoConnect: true, mtuAuto: false, listenPort: 0 })");
  await new Promise((r) => setTimeout(r, 10));
  assert.equal(ev(ctx, "state.settings.relayAddr"), undefined);
  assert.equal(ev(ctx, "state.settings.mtuAuto"), false);
});
