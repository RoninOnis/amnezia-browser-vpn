/* Панель администратора в расширении: команды, состояние и — главное —
 * обращение с паролем SSH.
 *
 * Пароль уходит в хелпер ровно один раз для первичной настройки и не должен
 * оседать ни в состоянии, ни в storage: иначе «не сохраняется» из интерфейса
 * становится ложью.
 *
 * Run: node --test extension/tests/panel.test.mjs
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

function stubBrowser({ replies = {} } = {}) {
  const noop = () => {};
  const promise = () => Promise.resolve();
  const sent = [];
  const stored = [];
  let messageHandler = null;

  const port = {
    postMessage(msg) {
      sent.push(msg);
      const reply = replies[msg.cmd] || { id: msg.id, ok: false, error: "no reply in test" };
      if (messageHandler) setTimeout(() => messageHandler({ ...reply, id: msg.id }), 0);
    },
    onMessage: { addListener: (fn) => { messageHandler = fn; } },
    onDisconnect: { addListener: noop },
    disconnect: noop,
  };

  const ctxBrowser = {
    proxy: { onRequest: { addListener: noop }, onError: { addListener: noop } },
    storage: {
      local: {
        get: async () => ({}),
        set: async (obj) => { stored.push(obj); },
      },
    },
    action: { setBadgeText: promise, setBadgeBackgroundColor: promise, setTitle: promise },
    alarms: { create: promise, onAlarm: { addListener: noop } },
    tabs: { onRemoved: { addListener: noop }, query: async () => [], create: noop },
    runtime: {
      lastError: null,
      onMessage: { addListener: noop },
      onStartup: { addListener: noop },
      onInstalled: { addListener: noop },
      sendMessage: async () => {},
      getPlatformInfo: async () => ({ os: "linux" }),
      getManifest: () => ({ version: "0.3.7" }),
      connectNative: () => port,
    },
    __sent: sent,
    __stored: stored,
  };
  return ctxBrowser;
}

async function boot(opts) {
  const ctx = vm.createContext({
    browser: stubBrowser(opts),
    console, setTimeout, clearTimeout, setInterval, clearInterval,
    atob, btoa, Blob, Response, TextDecoder, URL, DecompressionStream, Map, Promise, Date,
  });
  vm.runInContext(fs.readFileSync(path.join(extDir, "lib/vpnparse.js"), "utf8"), ctx, { filename: "vpnparse.js" });
  vm.runInContext(fs.readFileSync(path.join(extDir, "background.js"), "utf8"), ctx, { filename: "background.js" });
  await vm.runInContext("ready", ctx);
  return ctx;
}

const ev = (ctx, expr) => vm.runInContext(expr, ctx);

const PEER_LIST = {
  ok: true,
  peers: {
    container: "amnezia-awg2", config: "awg0.conf", listenPort: 36853,
    subnet: "10.8.1.0/24", serverPublicKey: "SRV=", params: ["Jc=5"],
    peers: [
      { publicKey: "PUB1=", allowedIps: "10.8.1.2/32", name: "ноутбук", lastHandshake: 0 },
      { publicKey: "PUB2=", allowedIps: "10.8.1.3/32", name: "телефон", lastHandshake: 0 },
    ],
  },
};

test("пароль SSH не остаётся ни в состоянии, ни в storage", async () => {
  const ctx = await boot({
    replies: {
      peer_bootstrap: { ok: true, peer: { selftest: '{"container":"amnezia-awg2"}' }, servers: [{ host: "vpn.example.com", port: 22 }] },
      peer_list: PEER_LIST,
    },
  });
  await ev(ctx, "state.helperMissing = false;");
  const res = plain(await ev(ctx, `bootstrapServer({ host: "vpn.example.com", port: 22, user: "root", password: "СуперСекрет123" })`));
  assert.equal(res.ok, true);

  const publicState = JSON.stringify(plain(ev(ctx, "publicState()")));
  assert.ok(!publicState.includes("СуперСекрет123"), "пароль попал в состояние для попапа");
  const storedDump = JSON.stringify(plain(ev(ctx, "browser.__stored")));
  assert.ok(!storedDump.includes("СуперСекрет123"), "пароль попал в storage.local");
  // но в хелпер он уйти обязан — иначе настройка не начнётся
  const msg = plain(ev(ctx, "browser.__sent")).find((m) => m.cmd === "peer_bootstrap");
  assert.equal(msg.password, "СуперСекрет123");
  assert.equal(msg.host, "vpn.example.com");
});

test("успешная настройка запоминает сервер и снимает флаг занятости", async () => {
  const ctx = await boot({
    replies: {
      peer_bootstrap: { ok: true, peer: {}, servers: [{ host: "vpn.example.com", port: 22 }] },
      peer_list: PEER_LIST,
    },
  });
  await ev(ctx, "state.helperMissing = false;");
  await ev(ctx, `bootstrapServer({ host: "vpn.example.com", password: "x" })`);
  assert.equal(ev(ctx, "state.peerBusy"), false);
  assert.equal(plain(ev(ctx, "state.servers")).length, 1);
  assert.equal(ev(ctx, "state.peerServer"), "vpn.example.com");
});

test("ошибка настройки доходит до пользователя понятным текстом", async () => {
  const ctx = await boot({
    replies: { peer_bootstrap: { ok: false, error: "сервер не принял логин или пароль" } },
  });
  await ev(ctx, "state.helperMissing = false;");
  const res = plain(await ev(ctx, `bootstrapServer({ host: "vpn.example.com", password: "плохой" })`));
  assert.equal(res.ok, false);
  assert.match(res.error, /не принял логин или пароль/);
  assert.equal(ev(ctx, "state.peerBusy"), false, "флаг занятости не должен залипнуть");
});

test("список ключей сохраняется вместе с именем сервера", async () => {
  const ctx = await boot({ replies: { peer_list: PEER_LIST } });
  await ev(ctx, "state.helperMissing = false; state.servers = [{ host: 'vpn.example.com' }];");
  const res = plain(await ev(ctx, "loadPeers('vpn.example.com')"));
  assert.equal(res.ok, true);
  const peers = plain(ev(ctx, "state.peers"));
  assert.equal(peers.peers.length, 2);
  assert.equal(peers.container, "amnezia-awg2");
  assert.equal(ev(ctx, "state.peerServer"), "vpn.example.com");
});

test("новый ключ: ссылка vpn:// попадает в состояние, приватный ключ не выдумывается", async () => {
  const ctx = await boot({
    replies: {
      peer_create: {
        ok: true,
        peer: { name: "планшет", allowedIps: "10.8.1.4/32", publicKey: "PUB3=", vpnLink: "vpn://AAA" },
      },
      peer_list: PEER_LIST,
    },
  });
  await ev(ctx, "state.helperMissing = false; state.peerServer = 'vpn.example.com';");
  const res = plain(await ev(ctx, `createPeer("планшет")`));
  assert.equal(res.ok, true);
  const key = plain(ev(ctx, "state.lastPeerKey"));
  assert.equal(key.vpnLink, "vpn://AAA");
  assert.equal(key.allowedIps, "10.8.1.4/32");
  assert.equal(ev(ctx, "state.peerBusy"), false);
});

test("ошибка выпуска ключа показывается и не оставляет «полуготовый» ключ", async () => {
  const ctx = await boot({
    replies: { peer_create: { ok: false, error: "в подсети 10.8.1.0/24 нет свободных адресов" } },
  });
  await ev(ctx, "state.helperMissing = false; state.peerServer = 'vpn.example.com';");
  const res = plain(await ev(ctx, `createPeer("тест")`));
  assert.equal(res.ok, false);
  assert.equal(ev(ctx, "state.lastPeerKey"), null);
  assert.match(ev(ctx, "state.peerError"), /нет свободных адресов/);
});

test("отзыв ключа убирает его из состояния", async () => {
  const ctx = await boot({
    replies: {
      peer_revoke: { ok: true },
      peer_list: PEER_LIST,
    },
  });
  await ev(ctx, "state.helperMissing = false; state.peerServer = 'vpn.example.com';");
  await ev(ctx, `state.lastPeerKey = { name: "x", publicKey: "PUB1=", vpnLink: "vpn://AAA" };`);
  const res = plain(await ev(ctx, `revokePeer("PUB1=")`));
  assert.equal(res.ok, true);
  assert.equal(ev(ctx, "state.lastPeerKey"), null, "отозванный ключ не должен предлагаться к установке");
  const msg = plain(ev(ctx, "browser.__sent")).find((m) => m.cmd === "peer_revoke");
  assert.equal(msg.publicKey, "PUB1=");
  assert.equal(msg.host, "vpn.example.com");
});

test("без сервера панель не дёргает хелпер", async () => {
  const ctx = await boot({});
  await ev(ctx, "state.helperMissing = false; state.servers = []; state.peerServer = '';");
  const res = plain(await ev(ctx, "loadPeers()"));
  assert.equal(res.ok, false);
  assert.match(res.error, /сервер не выбран/);
  assert.equal(plain(ev(ctx, "browser.__sent")).filter((m) => m.cmd === "peer_list").length, 0);
});
