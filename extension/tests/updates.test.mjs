/* Проверка версий аддона и хелпера.
 *
 * История, из-за которой это появилось: «аддон обновили, хелпер нет» — и человек
 * месяцами сидит на старом бинаре, потому что интерфейс показывал «всё хорошо».
 * Поэтому тесты фиксируют главное правило: без данных проверки никакого «актуально»
 * быть не должно, только честное «не удалось проверить».
 *
 * Run: node --test extension/tests/updates.test.mjs
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

// replies: { update_check: {...} } — что вернуть на конкретную команду
function stubBrowser({ replies = {}, storage = {}, version = "0.3.6.1" } = {}) {
  const noop = () => {};
  const promise = () => Promise.resolve();
  const store = JSON.parse(JSON.stringify(storage));
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
    storage: { local: { get: async () => store, set: async (o) => Object.assign(store, o) } },
    action: { setBadgeText: promise, setBadgeBackgroundColor: promise, setTitle: promise },
    alarms: { create: promise, onAlarm: { addListener: noop } },
    tabs: { onRemoved: { addListener: noop }, query: async () => [], create: noop },
    runtime: {
      lastError: null,
      onMessage: { addListener: (fn) => { ctxBrowser.__onMessage = fn; } },
      onStartup: { addListener: noop },
      onInstalled: { addListener: noop },
      sendMessage: async () => {},
      getPlatformInfo: async () => ({ os: "win" }),
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

test("сравнение версий: четвёртый компонент важнее, строки не путаются", async () => {
  const ctx = await boot({});
  assert.equal(ev(ctx, "compareVersions('0.3.6.1', '0.3.7')"), -1);
  assert.equal(ev(ctx, "compareVersions('0.3.6.1', '0.3.6')"), 1);
  assert.equal(ev(ctx, "compareVersions('0.3.6', '0.3.6.0')"), 0);
  assert.equal(ev(ctx, "compareVersions('0.10.0', '0.9.9')"), 1);
  assert.equal(ev(ctx, "compareVersions('v0.3.7', '0.3.7')"), 0);
  assert.equal(ev(ctx, "compareVersions('мусор', '0.3.7')"), null);
});

test("хелпер новее аддона: аддон помечен устаревшим, хелпер нет", async () => {
  const ctx = await boot({
    version: "0.3.6.1",
    replies: {
      update_check: {
        ok: true,
        update: {
          checked: true, latest: "0.3.7", helperLatest: "0.3.7",
          pageUrl: "https://example/release", publishedAt: "2026-11-20",
        },
      },
    },
  });
  await ev(ctx, "state.helperVersion = '0.3.7'; state.helperMissing = false;");
  const u = plain(await ev(ctx, "checkVersions(true)"));
  assert.equal(u.addon.current, "0.3.6.1");
  assert.equal(u.addon.latest, "0.3.7");
  assert.equal(u.addon.outdated, true, "аддон старше релиза");
  assert.equal(u.helper.current, "0.3.7");
  assert.equal(u.helper.outdated, false, "хелпер уже актуальный");
  assert.equal(u.error, "");
});

test("хелпер отстал от аддона — именно тот случай, из-за которого всё это делалось", async () => {
  const ctx = await boot({
    version: "0.3.7",
    replies: {
      update_check: {
        ok: true,
        update: { checked: true, latest: "0.3.7", helperLatest: "0.3.7", pageUrl: "https://example/r" },
      },
    },
  });
  await ev(ctx, "state.helperVersion = '0.3.6'; state.helperMissing = false;");
  const u = plain(await ev(ctx, "checkVersions(true)"));
  assert.equal(u.addon.outdated, false);
  assert.equal(u.helper.outdated, true, "старый хелпер обязан быть виден");
});

test("нет связи с GitHub — честное «не удалось», а не «всё актуально»", async () => {
  const ctx = await boot({
    version: "0.3.7",
    replies: { update_check: { ok: true, update: { checked: false, error: "нет связи с GitHub" } } },
  });
  await ev(ctx, "state.helperVersion = '0.3.6'; state.helperMissing = false;");
  const u = plain(await ev(ctx, "checkVersions(true)"));
  assert.equal(u.error, "нет связи с GitHub");
  assert.equal(u.addon.outdated, false, "без данных нельзя объявлять устаревшим");
  assert.equal(u.addon.unknown, true, "состояние должно быть «неизвестно»");
  assert.equal(u.helper.unknown, true);
});

test("хелпер не установлен — версия хелпера не выдумывается", async () => {
  const ctx = await boot({ version: "0.3.7" });
  await ev(ctx, "state.helperMissing = true; state.helperVersion = '';");
  const u = plain(await ev(ctx, "checkVersions(true)"));
  assert.equal(u.helper.missing, true);
  assert.equal(u.helper.current, "");
  assert.equal(u.helper.unknown, true);
  assert.match(u.error, /не установлен/);
});

test("принудительная проверка передаёт force в хелпер", async () => {
  const ctx = await boot({
    replies: { update_check: { ok: true, update: { checked: true, latest: "0.3.7" } } },
  });
  await ev(ctx, "state.helperMissing = false; state.helperVersion = '0.3.7';");
  await ev(ctx, "checkVersions(true)");
  const msg = plain(ev(ctx, "browser.__sent")).find((m) => m.cmd === "update_check");
  assert.ok(msg, "update_check не отправлен");
  assert.equal(msg.force, true);
});

test("результат проверки попадает в состояние для попапа", async () => {
  const ctx = await boot({
    replies: { update_check: { ok: true, update: { checked: true, latest: "0.3.7", pageUrl: "https://e/r" } } },
  });
  await ev(ctx, "state.helperMissing = false; state.helperVersion = '0.3.7';");
  await ev(ctx, "checkVersions(true)");
  const pub = plain(ev(ctx, "publicState()"));
  assert.ok(pub.updates, "updates должны быть в состоянии");
  assert.equal(pub.updates.addon.latest, "0.3.7");
  assert.equal(pub.updates.pageUrl, "https://e/r");
});
