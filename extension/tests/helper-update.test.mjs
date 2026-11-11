/* Обновление хелпера в один клик.
 *
 * Тяжёлую часть (скачивание, сверка sha256, подмена бинаря, перезапуск демона)
 * делает хелпер. Расширение отвечает за три вещи, и именно они проверяются:
 * кнопка запускает ровно одну операцию, результат виден пользователю честно,
 * а после успеха состояние перезапрашивается — иначе в попапе осталась бы
 * старая версия и человек решил бы, что обновление не сработало.
 *
 * Run: node --test extension/tests/helper-update.test.mjs
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

function stubBrowser({ replies = {}, version = "0.3.7" } = {}) {
  const noop = () => {};
  const promise = () => Promise.resolve();
  const sent = [];
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
    storage: { local: { get: async () => ({}), set: async () => {} } },
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
      getManifest: () => ({ version }),
      connectNative: () => port,
    },
    __sent: sent,
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

test("успешное обновление: версия выросла, статус сохранён, кэш версий сброшен", async () => {
  const ctx = await boot({
    replies: {
      self_update: {
        ok: true,
        selfUpdate: { from: "0.3.6", to: "0.3.7", asset: "awgbv-linux-amd64", bytes: 7295100, restarted: true },
      },
      update_check: { ok: true, update: { checked: true, latest: "0.3.7" } },
      refresh: { ok: true },
      status: { ok: true, status: { running: false, state: "stopped" } },
    },
  });
  await ev(ctx, "state.helperMissing = false; state.helperVersion = '0.3.6'; state.updates = { addon: {} };");
  const res = plain(await ev(ctx, "updateHelper()"));
  assert.equal(res.ok, true);
  assert.equal(res.result.to, "0.3.7");
  assert.equal(res.result.from, "0.3.6");
  assert.equal(ev(ctx, "state.helperVersion"), "0.3.7", "версия хелпера должна обновиться сразу");
  assert.equal(ev(ctx, "state.updates"), null, "кэш проверки версий должен сброситься");
  const msg = plain(ev(ctx, "browser.__sent")).find((m) => m.cmd === "self_update");
  assert.ok(msg, "self_update не отправлен");
});

test("неудачное обновление: причина показана, версия не подменена на выдуманную", async () => {
  const ctx = await boot({
    replies: {
      self_update: {
        ok: false,
        error: "sha256 не совпал: ожидался aaa, получен bbb",
        selfUpdate: { from: "0.3.6", asset: "awgbv-linux-amd64", error: "sha256 не совпал: ожидался aaa, получен bbb" },
      },
      refresh: { ok: true },
      status: { ok: true, status: { running: false, state: "stopped" } },
    },
  });
  await ev(ctx, "state.helperMissing = false; state.helperVersion = '0.3.6';");
  const res = plain(await ev(ctx, "updateHelper()"));
  assert.equal(res.ok, false);
  assert.match(res.error, /sha256/);
  assert.equal(ev(ctx, "state.helperVersion"), "0.3.6", "при ошибке версия остаётся прежней");
  assert.equal(plain(ev(ctx, "state.lastHelperUpdate")).ok, false);
});

test("второй запуск во время обновления не стартует", async () => {
  const ctx = await boot({
    replies: { self_update: { ok: true, selfUpdate: { to: "0.3.7" } }, refresh: { ok: true } },
  });
  await ev(ctx, "state.updatingHelper = true;");
  const res = plain(await ev(ctx, "updateHelper()"));
  assert.equal(res.ok, false);
  assert.match(res.error, /уже идёт/);
  const sent = plain(ev(ctx, "browser.__sent")).filter((m) => m.cmd === "self_update");
  assert.equal(sent.length, 0, "второй self_update уходить не должен");
});

test("флаг «идёт обновление» снимается в любом случае", async () => {
  const ctx = await boot({
    replies: {
      self_update: { ok: false, error: "нет связи с GitHub" },
      refresh: { ok: true },
      status: { ok: true, status: { running: false, state: "stopped" } },
    },
  });
  await ev(ctx, "state.helperMissing = false;");
  await ev(ctx, "updateHelper()");
  assert.equal(ev(ctx, "state.updatingHelper"), false, "флаг не должен залипнуть");
});

test("состояние для попапа содержит кнопку и результат последней попытки", async () => {
  const ctx = await boot({
    replies: {
      self_update: { ok: true, selfUpdate: { from: "0.3.6", to: "0.3.7", restarted: true } },
      refresh: { ok: true },
      status: { ok: true, status: { running: false, state: "stopped" } },
    },
  });
  await ev(ctx, "state.helperMissing = false; state.helperVersion = '0.3.6';");
  await ev(ctx, "updateHelper()");
  const pub = plain(ev(ctx, "publicState()"));
  assert.equal(pub.updatingHelper, false);
  assert.equal(pub.lastHelperUpdate.ok, true);
  assert.equal(pub.lastHelperUpdate.to, "0.3.7");
});
