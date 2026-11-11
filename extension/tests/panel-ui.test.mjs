/* Экран «Панель» в попапе: то, что видит пользователь.
 *
 * background проверяется в panel.test.mjs, здесь — рендер и обработчики попопа.
 * Поводы для тестов взяты из ревью 0.3.7:
 *  - хелпер отдаёт finished=true и для проверки (dry-run), поэтому порядок ветвей
 *    в подсказке — не косметика, а разница между «служба запущена» и правдой;
 *  - ошибки должны читаться цветом (.hint.err), а не серой справкой;
 *  - опрос каждые 4 секунды не имеет права переписывать поле со ссылкой ключа.
 *
 * Браузера нет: DOM подменён минимальным стендом (элементы по требованию,
 * обработчики кликов вызываются вручную), поэтому тесты быстрые и без зависимостей.
 *
 * Run: node --test extension/tests/panel-ui.test.mjs
 */
import assert from "node:assert/strict";
import { test } from "node:test";
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const extDir = path.resolve(here, "..");

/* ------------------------------------------------------------ мини-DOM */

function makeEl(tag = "div") {
  const classes = new Set();
  const children = [];
  const handlers = {};
  let text = "";
  let value = "";

  const node = {
    tagName: tag.toUpperCase(),
    children,
    handlers,
    dataset: {},
    hidden: false,
    disabled: false,
    checked: false,
    title: "",
    src: "",
    scrollTop: 0,
    __textWrites: 0,
    __valueWrites: 0,
    classList: {
      add: (...list) => list.forEach((c) => classes.add(c)),
      remove: (...list) => list.forEach((c) => classes.delete(c)),
      contains: (c) => classes.has(c),
      toggle: (c, force) => {
        const want = force === undefined ? !classes.has(c) : !!force;
        if (want) classes.add(c);
        else classes.delete(c);
        return want;
      },
    },
    appendChild(child) {
      children.push(child);
      return child;
    },
    attributes: {},
    setAttribute(name, val) {
      node.attributes[name] = String(val);
    },
    getAttribute(name) {
      return Object.prototype.hasOwnProperty.call(node.attributes, name)
        ? node.attributes[name]
        : null;
    },
    removeAttribute(name) {
      delete node.attributes[name];
    },
    addEventListener(type, fn) {
      (handlers[type] = handlers[type] || []).push(fn);
    },
    focus() {},
    scrollIntoView(arg) {
      node.__scrolled = arg === undefined ? true : arg;
    },
  };

  Object.defineProperty(node, "className", {
    get: () => Array.from(classes).join(" "),
    set: (v) => {
      classes.clear();
      String(v).split(/\s+/).filter(Boolean).forEach((c) => classes.add(c));
    },
  });
  Object.defineProperty(node, "textContent", {
    get: () => text,
    set: (v) => {
      node.__textWrites += 1;
      text = String(v);
      if (text === "") children.length = 0;
    },
  });
  Object.defineProperty(node, "innerHTML", {
    get: () => "",
    set: (v) => {
      if (String(v) === "") children.length = 0;
    },
  });
  Object.defineProperty(node, "value", {
    get: () => value,
    set: (v) => {
      node.__valueWrites += 1;
      value = String(v);
    },
  });
  return node;
}

function makeDocument() {
  const els = new Map();
  const doc = {
    __els: els,
    el(selector) {
      if (!els.has(selector)) els.set(selector, makeEl());
      return els.get(selector);
    },
    querySelector(selector) {
      return doc.el(selector);
    },
    querySelectorAll() {
      return [];
    },
    getElementById(id) {
      return doc.el(`#${id}`);
    },
    createElement(tag) {
      return makeEl(tag);
    },
  };
  return doc;
}

/* --------------------------------------------------------- окружение */

const BASE_STATE = {
  enabled: true,
  autoConnect: true,
  mode: "whitelist",
  helperVersion: "0.3.7",
  whitelist: [],
  blacklist: [],
  profiles: [{ id: "p1", name: "Helsinki", endpoint: "203.0.113.10:40000", container: "amnezia-awg2", clientIp: "10.99.7.14" }],
  activeProfileId: "p1",
  activeProfile: { id: "p1", name: "Helsinki", endpoint: "203.0.113.10:40000" },
  listenPort: 0,
  tunnel: { running: true, state: "connected", socksPort: 45678, lastHandshakeUnix: 0, rxBytes: 1024, txBytes: 512, uptimeSec: 60 },
  lastError: "",
  updates: null,
  updatingHelper: false,
  lastHelperUpdate: null,
  servers: [{ host: "vpn.example.com", port: 22 }],
  peers: {
    container: "amnezia-awg2",
    config: "awg0.conf",
    listenPort: 36853,
    subnet: "10.8.1.0/24",
    peers: [
      { publicKey: "PUB1=", allowedIps: "10.8.1.2/32", name: "ноутбук", lastHandshake: 0 },
      { publicKey: "PUB2=", allowedIps: "10.8.1.3/32", name: "телефон", lastHandshake: 0 },
    ],
  },
  peerServer: "vpn.example.com",
  peerBusy: false,
  peerError: "",
  lastPeerKey: null,
  satellite: null,
  satelliteBusy: false,
};

function makeState(patch = {}) {
  return { ...JSON.parse(JSON.stringify(BASE_STATE)), ...patch };
}

async function boot() {
  const doc = makeDocument();
  const sent = [];
  const dialogs = [];
  const env = {
    // попап должен получить состояние из ответа на refresh/getState
    state: null,
    confirmAnswer: true,
    promptAnswer: "планшет",
    replies: {},
  };

  const browser = {
    runtime: {
      getManifest: () => ({ version: "0.3.7" }),
      onMessage: { addListener: () => {} },
      sendMessage: async (msg) => {
        sent.push(msg);
        const custom = env.replies[msg.cmd];
        if (custom) return typeof custom === "function" ? custom(msg) : custom;
        if (msg.cmd === "listTabOverrides") return { ok: true, overrides: [] };
        if (msg.cmd === "getState" || msg.cmd === "refresh") return { ok: true, state: env.state };
        return { ok: true, state: env.state };
      },
    },
    tabs: { query: async () => [{ id: 1, url: "https://whatismyip.example/check" }], create: () => {} },
    downloads: { download: async () => 1 },
  };

  const ctx = vm.createContext({
    browser,
    document: doc,
    console,
    setTimeout,
    clearTimeout,
    // опрос раз в 4 секунды в тестах не нужен: обновляем состояние вручную
    setInterval: () => 1,
    clearInterval: () => {},
    confirm: (text) => {
      dialogs.push(text);
      return env.confirmAnswer;
    },
    prompt: () => env.promptAnswer,
    navigator: { clipboard: { writeText: async () => {} } },
    URL,
    Map,
    Promise,
    Date,
    JSON,
    String,
    Number,
    Array,
    Object,
    Math,
  });
  vm.runInContext(fs.readFileSync(path.join(extDir, "popup.js"), "utf8"), ctx, { filename: "popup.js" });
  await new Promise((resolve) => setTimeout(resolve, 0)); // дать init() договорить

  return { ctx, doc, sent, dialogs, env };
}

const ev = (ctx, expr) => vm.runInContext(expr, ctx);

async function renderWith(ui, state) {
  ui.env.state = state;
  await ev(ui.ctx, "refresh()");
  return ui;
}

async function click(ui, node) {
  for (const fn of node.handlers.click || []) await fn();
}

const el = (ui, selector) => ui.doc.el(selector);

/* ------------------------------------------------------------------ тесты */

test("«Проверить» не рапортует об установке, даже когда хелпер вернул finished=true", async () => {
  const ui = await boot();
  await renderWith(ui, makeState({
    satellite: { dryRun: true, finished: true, output: "цель: 172.29.172.2:37689" },
  }));

  const hint = el(ui, "#satHint");
  assert.match(hint.textContent, /Проверка прошла/);
  assert.ok(!/запущена/.test(hint.textContent), "проверка не запускает службу");
  assert.equal(hint.classList.contains("err"), false);
});

test("проверка с проблемой показывается красным и ссылается на вывод", async () => {
  const ui = await boot();
  await renderWith(ui, makeState({
    satellite: { dryRun: true, finished: false, output: "порт 443/udp уже занят: awgbv-satellite" },
  }));

  const hint = el(ui, "#satHint");
  assert.equal(hint.classList.contains("err"), true, "ошибка должна быть красной");
  assert.match(hint.textContent, /ничего не изменено/);
  assert.match(hint.textContent, /ниже/, "вывод есть — на него можно сослаться");
  assert.equal(el(ui, "#satOutput").hidden, false);
  assert.match(el(ui, "#satOutput").textContent, /443\/udp уже занят/);
});

test("установка без правки firewall говорит об этом прямо", async () => {
  const ui = await boot();
  await renderWith(ui, makeState({
    satellite: { dryRun: false, finished: true, openFirewall: false, output: "служба awgbv-satellite запущена" },
  }));

  const hint = el(ui, "#satHint");
  assert.match(hint.textContent, /firewall не менялся/i);
  assert.equal(hint.classList.contains("warn"), true, "оговорку видно, но это не ошибка");
  assert.equal(hint.classList.contains("err"), false);
});

test("установка с открытым firewall не извиняется за порт", async () => {
  const ui = await boot();
  await renderWith(ui, makeState({
    satellite: { dryRun: false, finished: true, openFirewall: true, output: "служба awgbv-satellite запущена" },
  }));

  const hint = el(ui, "#satHint");
  assert.match(hint.textContent, /UDP\/443 открыт/);
  assert.equal(hint.classList.contains("warn"), false, "тут извиняться не за что");
});

test("ошибка установщика без вывода не обещает «подробности ниже»", async () => {
  const ui = await boot();
  await renderWith(ui, makeState({
    satellite: { dryRun: false, finished: false, output: "" },
  }));

  const hint = el(ui, "#satHint");
  assert.equal(hint.classList.contains("err"), true);
  assert.ok(!/ниже/.test(hint.textContent), "вывода нет — ссылаться не на что");
  assert.equal(el(ui, "#satOutput").hidden, true);
});

test("ключи на сервере рисуются строками списка, а не голым текстом", async () => {
  const ui = await boot();
  await renderWith(ui, makeState());

  const rows = el(ui, "#admPeers").children;
  assert.equal(rows.length, 2);
  for (const row of rows) {
    assert.ok(row.classList.contains("list-item"), "строка ключа должна быть .list-item");
    const button = row.children[row.children.length - 1];
    assert.ok(button.classList.contains("btn"));
    assert.ok(button.classList.contains("danger"));
    assert.equal(button.textContent, "Отозвать");
    assert.notEqual(button.getAttribute("aria-disabled"), "true");
  }
});

test("во время SSH-операции отключены все кнопки панели, а не только «Отозвать»", async () => {
  const ui = await boot();
  await renderWith(ui, makeState({ peerBusy: true }));

  for (const selector of [
    "#btnAdminConnect", "#btnAdminRefresh", "#btnAdminCreate",
    "#btnAdminCopy", "#btnAdminAdopt", "#btnSatPlan", "#btnSatInstall",
  ]) {
    assert.equal(el(ui, selector).getAttribute("aria-disabled"), "true",
      `${selector} должен быть помечен занятым`);
  }
  const revoke = el(ui, "#admPeers").children[0].children.at(-1);
  assert.equal(revoke.getAttribute("aria-disabled"), "true");

  await renderWith(ui, makeState({ satelliteBusy: true }));
  assert.equal(el(ui, "#btnSatInstall").getAttribute("aria-disabled"), "true",
    "занятость спутника тоже помечает панель");
  assert.equal(el(ui, "#btnAdminCreate").getAttribute("aria-disabled"), "true");
});

test("ошибка панели показывается красным .hint.err", async () => {
  const ui = await boot();
  await renderWith(ui, makeState({ peers: null, peerError: "сервер не принял логин или пароль" }));

  const hint = el(ui, "#admPeerHint");
  assert.equal(hint.textContent, "сервер не принял логин или пароль");
  assert.equal(hint.classList.contains("err"), true);
});

test("при настроенном сервере форма SSH скрыта, а список честно пишет «Загружаю ключи…»", async () => {
  const ui = await boot();
  await renderWith(ui, makeState({ peers: null }));

  assert.equal(el(ui, "#adminConnectCard").hidden, true, "пароль SSH не спрашивают заново");
  assert.equal(el(ui, "#adminPeersCard").hidden, false);
  assert.equal(el(ui, "#admPeerHint").textContent, "Загружаю ключи…");
});

test("без серверов показывается форма первичной настройки", async () => {
  const ui = await boot();
  await renderWith(ui, makeState({ servers: [], peers: null, peerServer: "" }));

  assert.equal(el(ui, "#adminConnectCard").hidden, false);
  assert.equal(el(ui, "#adminPeersCard").hidden, true);
});

test("кнопка «другой сервер» раскрывает форму подключения при готовом сервере", async () => {
  const ui = await boot();
  await renderWith(ui, makeState());
  assert.equal(el(ui, "#adminConnectCard").hidden, true);

  await click(ui, el(ui, "#btnAdminAnother"));
  assert.equal(el(ui, "#adminConnectCard").hidden, false, "иначе сменить адрес/пароль негде");
});

test("контейнер показывается с портом, а подсеть — отдельной строкой", async () => {
  const ui = await boot();
  await renderWith(ui, makeState());

  assert.equal(el(ui, "#admContainer").textContent, "amnezia-awg2 · udp/36853");
  assert.equal(el(ui, "#admSubnet").textContent, "10.8.1.0/24");
});

test("адрес сервера подставляется в поля панели, чтобы не вводить его заново", async () => {
  const ui = await boot();
  await renderWith(ui, makeState());

  assert.equal(el(ui, "#satHost").value, "vpn.example.com");
  assert.equal(el(ui, "#admHost").value, "vpn.example.com");
});

test("опрос не переписывает поле со ссылкой ключа и вывод установщика", async () => {
  const ui = await boot();
  const state = makeState({
    satellite: { dryRun: true, finished: true, output: "план" },
    lastPeerKey: { name: "планшет", allowedIps: "10.8.1.4/32", publicKey: "PUB3=", vpnLink: "vpn://AAA", adopted: false },
  });
  await renderWith(ui, state);

  const link = el(ui, "#admKeyLink");
  const output = el(ui, "#satOutput");
  assert.equal(link.value, "vpn://AAA");
  const linkWrites = link.__valueWrites;
  const outWrites = output.__textWrites;

  await renderWith(ui, makeState({
    satellite: { dryRun: true, finished: true, output: "план" },
    lastPeerKey: { name: "планшет", allowedIps: "10.8.1.4/32", publicKey: "PUB3=", vpnLink: "vpn://AAA", adopted: false },
  }));
  await renderWith(ui, makeState({
    satellite: { dryRun: true, finished: true, output: "план" },
    lastPeerKey: { name: "планшет", allowedIps: "10.8.1.4/32", publicKey: "PUB3=", vpnLink: "vpn://AAA", adopted: false },
  }));

  assert.equal(link.__valueWrites, linkWrites, "переписывание сбрасывает выделение текста");
  assert.equal(output.__textWrites, outWrites, "переписывание отбрасывает прокрутку вывода");
});

test("«Проверить» очищает пароль и не просит открывать firewall", async () => {
  const ui = await boot();
  await renderWith(ui, makeState());
  el(ui, "#satHost").value = "vpn.example.com";
  el(ui, "#satPassword").value = "СуперСекрет";
  el(ui, "#satFirewall").checked = true;

  await click(ui, el(ui, "#btnSatPlan"));

  const msg = ui.sent.filter((m) => m.cmd === "satellitePlan").pop();
  assert.equal(msg.password, "СуперСекрет");
  assert.equal(msg.openFirewall, false, "проверка ничего не меняет");
  assert.equal(el(ui, "#satPassword").value, "", "пароль не остаётся в поле");
});

test("«Установить» очищает пароль и передаёт просьбу открыть UDP/443", async () => {
  const ui = await boot();
  await renderWith(ui, makeState());
  el(ui, "#satHost").value = "vpn.example.com";
  el(ui, "#satPassword").value = "СуперСекрет";
  el(ui, "#satFirewall").checked = true;

  await click(ui, el(ui, "#btnSatInstall"));

  const msg = ui.sent.filter((m) => m.cmd === "satelliteInstall").pop();
  assert.equal(msg.openFirewall, true);
  assert.equal(el(ui, "#satPassword").value, "");
  assert.equal(ui.dialogs.some((d) => /awgbv-satellite/.test(d)), true, "перед установкой спрашиваем");
});

test("без пароля SSH панель не отправляет операцию в хелпер", async () => {
  const ui = await boot();
  await renderWith(ui, makeState());
  el(ui, "#satHost").value = "vpn.example.com";
  el(ui, "#satPassword").value = "";

  await click(ui, el(ui, "#btnSatPlan"));

  assert.equal(ui.sent.filter((m) => m.cmd === "satellitePlan").length, 0);
  assert.equal(el(ui, "#satHint").classList.contains("err"), true);
});

test("после выпуска ключа попап прокручивает к карточке результата", async () => {
  const ui = await boot();
  const state = makeState();
  await renderWith(ui, state);
  ui.env.state = makeState({
    lastPeerKey: { name: "планшет", allowedIps: "10.8.1.4/32", publicKey: "PUB3=", vpnLink: "vpn://AAA", adopted: false },
  });
  ui.env.replies.createPeer = () => ({ ok: true, state: ui.env.state });

  await click(ui, el(ui, "#btnAdminCreate"));

  assert.ok(el(ui, "#adminKeyCard").__scrolled, "карточку с ключом иначе не найти при десятке пиров");
  assert.equal(el(ui, "#adminKeyCard").hidden, false);
  assert.equal(el(ui, "#admKeyLink").value, "vpn://AAA");
});

test("после неудачи выпуска показывается ошибка, а не пустая карточка ключа", async () => {
  const ui = await boot();
  await renderWith(ui, makeState());
  ui.env.state = makeState({ peers: null, peerError: "в подсети 10.8.1.0/24 нет свободных адресов" });
  ui.env.replies.createPeer = () => ({
    ok: false,
    error: "в подсети 10.8.1.0/24 нет свободных адресов",
    state: ui.env.state,
  });

  await click(ui, el(ui, "#btnAdminCreate"));

  assert.equal(el(ui, "#adminKeyCard").hidden, true);
  const hint = el(ui, "#admPeerHint");
  assert.equal(hint.classList.contains("err"), true);
  assert.match(hint.textContent, /нет свободных адресов/);
});

test("«Добавить в мои серверы» пропадает, когда ключ уже добавлен", async () => {
  const ui = await boot();
  await renderWith(ui, makeState({
    lastPeerKey: { name: "планшет", allowedIps: "10.8.1.4/32", publicKey: "PUB3=", vpnLink: "vpn://AAA", adopted: false },
  }));
  assert.equal(el(ui, "#btnAdminAdopt").hidden, false);

  await renderWith(ui, makeState({
    lastPeerKey: { name: "планшет", allowedIps: "10.8.1.4/32", publicKey: "PUB3=", vpnLink: "vpn://AAA", adopted: true },
  }));
  assert.equal(el(ui, "#btnAdminAdopt").hidden, true, "второй раз добавлять нечего");
});

test("вопрос об отзыве называет ключ даже без имени и адреса", async () => {
  const ui = await boot();
  await renderWith(ui, makeState({
    peers: {
      container: "amnezia-awg2",
      listenPort: 36853,
      subnet: "10.8.1.0/24",
      peers: [{ publicKey: "ABCDEFGH12345678", allowedIps: "", name: "", lastHandshake: 0 }],
    },
  }));
  ui.env.confirmAnswer = false; // отзыв не подтверждаем: проверяем только текст вопроса

  await click(ui, el(ui, "#admPeers").children[0].children.at(-1));

  const question = ui.dialogs.at(-1);
  assert.match(question, /ABCDEFGH/, "иначе спрашиваем «Отозвать ключ ?»");
  assert.match(question, /потеряет доступ/);
});

test("ошибка отзыва показывается красным", async () => {
  const ui = await boot();
  await renderWith(ui, makeState());
  ui.env.confirmAnswer = true;
  ui.env.replies.revokePeer = () => ({
    ok: false,
    error: "ключ не найден на сервере",
    state: makeState(),
  });

  await click(ui, el(ui, "#admPeers").children[0].children.at(-1));

  const hint = el(ui, "#admPeerHint");
  assert.equal(hint.classList.contains("err"), true);
  assert.match(hint.textContent, /ключ не найден/);
});
