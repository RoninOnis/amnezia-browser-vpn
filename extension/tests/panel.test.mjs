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
  const session = {}; // browser.storage.session: живёт до закрытия браузера
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
      session: {
        get: async (key) => (key in session ? { [key]: session[key] } : {}),
        set: async (obj) => { Object.assign(session, JSON.parse(JSON.stringify(obj))); },
        remove: async (key) => { delete session[key]; },
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
    __session: session,
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

// Реальный отказ хелпера выглядит так: reply.ok=false И peer.ok=false, ссылки нет
// (до 0.3.7 бывало ok=true с peer.ok=false — эту ветку тоже проверяем ниже).
test("ошибка выпуска ключа показывается и не оставляет «полуготовый» ключ", async () => {
  const ctx = await boot({
    replies: {
      peer_create: {
        ok: false,
        error: "в подсети 10.8.1.0/24 нет свободных адресов",
        peer: { ok: false, error: "в подсети 10.8.1.0/24 нет свободных адресов", vpnLink: "" },
      },
    },
  });
  await ev(ctx, "state.helperMissing = false; state.peerServer = 'vpn.example.com';");
  const res = plain(await ev(ctx, `createPeer("тест")`));
  assert.equal(res.ok, false);
  assert.equal(ev(ctx, "state.lastPeerKey"), null);
  assert.match(ev(ctx, "state.peerError"), /нет свободных адресов/);
});

// Переходный случай: хелпер отвечает ok=true (так делает nm.go до 0.3.8), но
// собрать vpn:// не смог — peer.ok=false и пустой vpnLink. Раньше попап показывал
// «Новый ключ готов» с пустым полем, и ключ терялся без единой ошибки.
test("ключ без ссылки vpn:// не считается выпущенным, даже если хелпер сказал ok", async () => {
  const ctx = await boot({
    replies: {
      peer_create: { ok: true, peer: { ok: false, error: "не удалось собрать ссылку vpn://", vpnLink: "" } },
    },
  });
  await ev(ctx, "state.helperMissing = false; state.peerServer = 'vpn.example.com';");
  const res = plain(await ev(ctx, `createPeer("планшет")`));
  assert.equal(res.ok, false, "пустая ссылка — это не успех");
  assert.equal(ev(ctx, "state.lastPeerKey"), null);
  assert.match(res.error, /ссылк|vpn:\/\//);
  assert.match(ev(ctx, "state.peerError"), /ссылк|vpn:\/\//);
});

test("ключ без ссылки не уходит и в storage.session", async () => {
  const ctx = await boot({
    replies: { peer_create: { ok: true, peer: { ok: true, vpnLink: "" } } },
  });
  await ev(ctx, "state.helperMissing = false; state.peerServer = 'vpn.example.com';");
  await ev(ctx, `createPeer("планшет")`);
  assert.deepEqual(plain(ev(ctx, "browser.__session")), {});
});

// Ключ — единственная копия ссылки: сервер её не хранит. Перезапуск event-страницы
// (она живёт секунды) не должен стирать её из попапа.
test("выпущенный ключ хранится в storage.session и переживает перезапуск страницы", async () => {
  const ctx = await boot({
    replies: {
      peer_create: {
        ok: true,
        peer: { ok: true, name: "планшет", allowedIps: "10.8.1.4/32", publicKey: "PUB3=", vpnLink: "vpn://AAA" },
      },
      peer_list: PEER_LIST,
    },
  });
  await ev(ctx, "state.helperMissing = false; state.peerServer = 'vpn.example.com';");
  await ev(ctx, `createPeer("планшет")`);
  const saved = plain(ev(ctx, "browser.__session.lastPeerKey"));
  assert.equal(saved.vpnLink, "vpn://AAA");
  assert.equal(saved.adopted, false);

  // имитируем новую сессию event-страницы: память пуста, storage.session — нет
  await ev(ctx, "state.lastPeerKey = null;");
  await ev(ctx, "loadSessionKey()");
  const restored = plain(ev(ctx, "state.lastPeerKey"));
  assert.equal(restored.vpnLink, "vpn://AAA", "ссылка должна вернуться из storage.session");
});

test("отзыв ключа убирает его и из storage.session", async () => {
  const ctx = await boot({ replies: { peer_revoke: { ok: true }, peer_list: PEER_LIST } });
  await ev(ctx, "state.helperMissing = false; state.peerServer = 'vpn.example.com';");
  await ev(ctx, `state.lastPeerKey = { name: "x", publicKey: "PUB1=", vpnLink: "vpn://AAA" };`);
  await ev(ctx, `saveSessionKey(state.lastPeerKey)`);
  assert.equal(plain(ev(ctx, "browser.__session.lastPeerKey.vpnLink")), "vpn://AAA");
  await ev(ctx, `revokePeer("PUB1=")`);
  assert.equal(ev(ctx, "browser.__session.lastPeerKey"), undefined);
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

// Настоящий ответ хелпера на dry-run приходит с finished=true (скрипт отработал
// без ошибок) — именно поэтому попап обязан смотреть на dryRun первым. Здесь
// проверяем, что состояние сохраняет оба поля, а не «схлопывает» их в одно.
test("спутник: проверка идёт как dry-run, установка — без него", async () => {
  const ctx = await boot({
    replies: {
      satellite_plan: { ok: true, satellite: { dryRun: true, finished: true, output: "план" } },
      satellite_install: { ok: true, satellite: { dryRun: false, finished: true, output: "служба запущена" } },
    },
  });
  await ev(ctx, "state.helperMissing = false;");
  await ev(ctx, `runSatellite({ host: "vpn.example.com", password: "Секрет", dryRun: true })`);
  const planMsg = plain(ev(ctx, "browser.__sent")).filter((m) => m.cmd === "satellite_plan").pop();
  assert.equal(planMsg.dryRun, true);
  assert.equal(ev(ctx, "state.satelliteBusy"), false);
  const planState = plain(ev(ctx, "state.satellite"));
  assert.equal(planState.dryRun, true);
  assert.equal(planState.finished, true, "хелпер ставит finished=true и для плана");
  assert.equal(planState.openFirewall, false, "проверка ничего не открывает");

  await ev(ctx, `runSatellite({ host: "vpn.example.com", password: "Секрет", dryRun: false })`);
  const instMsg = plain(ev(ctx, "browser.__sent")).filter((m) => m.cmd === "satellite_install").pop();
  assert.equal(instMsg.dryRun, false);
  const sat = plain(ev(ctx, "state.satellite"));
  assert.equal(sat.finished, true);
  // пароль не должен осесть в состоянии
  assert.ok(!JSON.stringify(plain(ev(ctx, "publicState()"))).includes("Секрет"));
});

// «Firewall не менялся» — это факт, а не оговорка: попап не должен обещать открытый
// порт, когда его не открывали.
test("спутник: факт правки firewall запоминается только для установки", async () => {
  const ctx = await boot({
    replies: {
      satellite_plan: { ok: true, satellite: { dryRun: true, finished: true, output: "план" } },
      satellite_install: { ok: true, satellite: { dryRun: false, finished: true, output: "служба запущена" } },
    },
  });
  await ev(ctx, "state.helperMissing = false;");
  await ev(ctx, `runSatellite({ host: "vpn.example.com", dryRun: true, openFirewall: true })`);
  assert.equal(ev(ctx, "state.satellite.openFirewall"), false);
  await ev(ctx, `runSatellite({ host: "vpn.example.com", dryRun: false, openFirewall: false })`);
  assert.equal(ev(ctx, "state.satellite.openFirewall"), false);
  await ev(ctx, `runSatellite({ host: "vpn.example.com", dryRun: false, openFirewall: true })`);
  assert.equal(ev(ctx, "state.satellite.openFirewall"), true);
  const msg = plain(ev(ctx, "browser.__sent")).filter((m) => m.cmd === "satellite_install").pop();
  assert.equal(msg.openFirewall, true, "просьба открыть порт должна дойти до установщика");
});

// Отказ установщика («порт занят», «чужой контейнер») — это не «молчание хелпера»:
// пользователь обязан увидеть текст, иначе он не поймёт, что чинить.
test("спутник: отказ установщика доходит текстом, а не теряется", async () => {
  const ctx = await boot({
    replies: {
      satellite_plan: {
        ok: false,
        error: "порт 443/udp уже занят: awgbv-satellite",
        satellite: { dryRun: true, finished: false, output: "порт 443/udp уже занят: awgbv-satellite" },
      },
    },
  });
  await ev(ctx, "state.helperMissing = false;");
  const res = plain(await ev(ctx, `runSatellite({ host: "vpn.example.com", dryRun: true })`));
  assert.equal(res.ok, false);
  const sat = plain(ev(ctx, "state.satellite"));
  assert.match(sat.output, /443\/udp уже занят/);
  assert.equal(sat.finished, false);
});

test("спутник: если хелпер вообще не ответил, причина тоже не теряется", async () => {
  const ctx = await boot({ replies: { satellite_install: { ok: false, error: "нет ответа от хелпера" } } });
  await ev(ctx, "state.helperMissing = false;");
  await ev(ctx, `runSatellite({ host: "vpn.example.com", dryRun: false })`);
  const sat = plain(ev(ctx, "state.satellite"));
  assert.match(sat.output, /нет ответа/);
  assert.equal(ev(ctx, "state.satelliteBusy"), false);
});

test("без сервера панель не дёргает хелпер", async () => {
  const ctx = await boot({});
  await ev(ctx, "state.helperMissing = false; state.servers = []; state.peerServer = '';");
  const res = plain(await ev(ctx, "loadPeers()"));
  assert.equal(res.ok, false);
  assert.match(res.error, /сервер не выбран/);
  assert.equal(plain(ev(ctx, "browser.__sent")).filter((m) => m.cmd === "peer_list").length, 0);
});

// ---------------------------------------------------------------- adopt / профили

const PANEL_LINK = fs.readFileSync(path.join(here, "fixtures", "panel-link.txt"), "utf8").trim();

test("ключ из панели добавляется в «Серверы» один раз, повторный adopt не плодит дубликат", async () => {
  const ctx = await boot({});
  await ev(ctx, "state.helperMissing = false; state.settings.profiles = [];");
  await ev(ctx, `saveSessionKey({ name: "панель", allowedIps: "10.8.1.9/32", publicKey: "PUB9=", vpnLink: ${JSON.stringify(PANEL_LINK)} })`);

  const first = plain(await ev(ctx, "adoptPanelKey()"));
  assert.equal(first.ok, true);
  assert.equal(first.duplicate, undefined, "первый раз ключ добавляется по-настоящему");
  assert.equal(plain(ev(ctx, "state.settings.profiles")).length, 1);
  assert.equal(ev(ctx, "state.lastPeerKey.adopted"), true, "попап по этому флагу прячет кнопку");

  const second = plain(await ev(ctx, "adoptPanelKey()"));
  assert.equal(second.ok, true);
  assert.equal(second.duplicate, true, "повторный adopt обязан вернуть уже существующий профиль");
  assert.equal(second.id, first.id);
  assert.equal(plain(ev(ctx, "state.settings.profiles")).length, 1, "второго профиля быть не должно");
});

test("тот же ключ, добавленный вручную на вкладке «Серверы», тоже не дублируется", async () => {
  const ctx = await boot({});
  await ev(ctx, "state.helperMissing = false; state.settings.profiles = [];");
  const a = plain(await ev(ctx, `addProfile(${JSON.stringify(PANEL_LINK)})`));
  const b = plain(await ev(ctx, `addProfile(${JSON.stringify(PANEL_LINK)})`));
  assert.equal(a.id, b.id);
  assert.equal(b.duplicate, true);
  assert.equal(plain(ev(ctx, "state.settings.profiles")).length, 1);
});

// ------------------------------------------------- автозагрузка списка ключей

// После перезапуска браузера сервер уже настроен, а список ключей пуст. Раньше
// попап показывал пустой список и форму с SSH-паролем, будто сервера нет.
test("refresh сам подгружает ключи, если сервер уже настроен", async () => {
  const ctx = await boot({ replies: { peer_list: PEER_LIST } });
  await ev(ctx, "state.helperMissing = false; state.servers = [{ host: 'vpn.example.com', port: 22 }];");
  await ev(ctx, "state.peers = null; state.peerServer = ''; state.peerError = '';");

  await ev(ctx, "refreshStatus()");

  const listMsgs = plain(ev(ctx, "browser.__sent")).filter((m) => m.cmd === "peer_list");
  assert.equal(listMsgs.length, 1, "список ключей должен запрашиваться сам");
  assert.equal(listMsgs[0].host, "vpn.example.com");
  assert.equal(plain(ev(ctx, "state.peers")).peers.length, 2);
});

test("после ошибки автозагрузка не долбит сервер каждый опрос", async () => {
  const ctx = await boot({});
  await ev(ctx, "state.helperMissing = false; state.servers = [{ host: 'vpn.example.com' }];");
  await ev(ctx, "state.peers = null; state.peerServer = ''; state.peerError = '';");

  await ev(ctx, "refreshStatus()"); // хелпер не ответил — ошибка запомнена
  assert.equal(ev(ctx, "state.peers"), null);
  const after = plain(ev(ctx, "browser.__sent")).filter((m) => m.cmd === "peer_list").length;
  assert.equal(after, 1);

  await ev(ctx, "refreshStatus()");
  assert.equal(
    plain(ev(ctx, "browser.__sent")).filter((m) => m.cmd === "peer_list").length,
    after,
    "повторный опрос не должен снова идти за ключами",
  );
});
