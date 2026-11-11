/* Тесты «как пользователь»: неаккуратный, но абсолютно нормальный ввод.
 *
 * Поводы взяты не из кода, а из наблюдения за живым интерфейсом: человек вставляет
 * адрес из браузера целиком, ставит запятую между доменами, оставляет пустым поле
 * порта, жмёт кнопку во время фоновой операции. Раньше на такое либо не было
 * ответа вообще, либо ответ был техническим («bad json», «String contains an
 * invalid character»), либо в список попадал мусор, который ничего не фильтрует.
 *
 * Run: node --test extension/tests/naive-user.test.mjs
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
    addEventListener(type, fn) {
      (handlers[type] = handlers[type] || []).push(fn);
    },
    focus() {},
    scrollIntoView() {},
    click() {
      for (const fn of handlers.click || []) fn.call(node, {});
    },
  };

  Object.defineProperty(node, "className", {
    get: () => [...classes].join(" "),
    set: (v) => {
      classes.clear();
      String(v || "").split(/\s+/).filter(Boolean).forEach((c) => classes.add(c));
    },
  });
  Object.defineProperty(node, "textContent", {
    get: () => text,
    set: (v) => {
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
      value = String(v);
    },
  });
  return node;
}

function makeDocument() {
  const els = new Map();
  const doc = {
    el(selector) {
      if (!els.has(selector)) els.set(selector, makeEl());
      return els.get(selector);
    },
    querySelector: (s) => doc.el(s),
    querySelectorAll: () => [],
    getElementById: (id) => doc.el(`#${id}`),
    createElement: (tag) => makeEl(tag),
  };
  return doc;
}

const BASE_STATE = {
  enabled: true,
  autoConnect: false,
  mode: "whitelist",
  helperVersion: "0.3.7",
  whitelist: [],
  blacklist: [],
  profiles: [],
  activeProfileId: "",
  activeProfile: null,
  listenPort: 0,
  tunnel: { running: false, state: "stopped" },
  lastError: "",
  updates: null,
  servers: [{ host: "vpn.example.com", port: 22 }],
  peers: null,
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
  const env = { state: null, replies: {}, promptAnswer: "ноутбук", confirmAnswer: true };

  const browser = {
    runtime: {
      getManifest: () => ({ version: "0.3.7" }),
      onMessage: { addListener: () => {} },
      sendMessage: async (msg) => {
        sent.push(msg);
        const custom = env.replies[msg.cmd];
        if (custom) return typeof custom === "function" ? custom(msg) : custom;
        if (msg.cmd === "listTabOverrides") return { ok: true, overrides: [] };
        if (msg.cmd === "addProfile") return { ok: true, id: "pX" };
        if (msg.cmd === "saveLists") {
          // как настоящее хранилище: попап после сохранения перечитывает состояние
          env.state = { ...env.state, ...msg };
        }
        return { ok: true, state: env.state };
      },
    },
    tabs: { query: async () => [{ id: 1, url: "https://example.com/" }], create: () => {} },
    downloads: { download: async () => 1 },
  };

  const ctx = vm.createContext({
    browser,
    document: doc,
    console,
    setTimeout,
    clearTimeout,
    setInterval: () => 1,
    clearInterval: () => {},
    confirm: (text) => {
      dialogs.push(text);
      return env.confirmAnswer;
    },
    prompt: () => env.promptAnswer,
    navigator: { clipboard: { writeText: async () => {} } },
    fetch: async () => ({ ok: true, json: async () => ({}) }),
    URL,
    Map,
    Set,
    Promise,
    Date,
    JSON,
    String,
    Number,
    Array,
    Object,
    Math,
    RegExp,
    Error,
    Uint8Array,
    TextDecoder,
    DecompressionStream: class {},
    Blob: class {},
    Response: class {},
  });

  // общий код разбора vpn://, затем сам попап
  vm.runInContext(fs.readFileSync(path.join(extDir, "lib", "vpnparse.js"), "utf8"), ctx);
  vm.runInContext(fs.readFileSync(path.join(extDir, "popup.js"), "utf8"), ctx);
  // обработчики DOMContentLoaded в попапе навешиваются сразу — здесь достаточно
  // того, что модуль выполнился и функции доступны через контекст
  return { ctx, doc, el: (s) => doc.el(s), sent, dialogs, env };
}

async function renderWith(ui, state) {
  ui.env.state = state;
  await ui.ctx.refresh();
  return ui;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// переменные попапа объявлены через let/const — они не свойства контекста,
// поэтому читаем их выражением внутри vm
const ctxVar = (ui, name) => vm.runInContext(name, ui.ctx);

async function click(ui, node) {
  for (const fn of node.handlers.click || []) await fn.call(node, {});
}

/* --------------------------------------------------------------- тесты */

test("адрес сайта приводится к домену так, как его вставляют люди", async () => {
  const ui = await boot();
  const cases = [
    ["https://youtube.com/watch?v=abc", "youtube.com"],
    ["  VK.COM  ", "vk.com"],
    ["example.com/", "example.com"],
    ["user@example.com", "example.com"],
    ["*.example.org", "*.example.org"],
    ["яндекс.рф", "xn--d1acpjx3f.xn--p1ai"],
    ["localhost", "localhost"],
    ["192.168.1.1", "192.168.1.1"],
  ];
  for (const [input, want] of cases) {
    assert.equal(ui.ctx.normalizeHost(input), want, `normalizeHost(${JSON.stringify(input)})`);
  }
});

test("текст вместо адреса не попадает в список и получает объяснение", async () => {
  const ui = await boot();
  await renderWith(ui, makeState());
  for (const junk of ["не url", "два через запятую", "!!!", "youtube"]) {
    ui.el("#siteInput").value = junk;
    await click(ui, ui.el("#btnAddSite"));
    const saved = ui.sent.filter((m) => m.cmd === "saveLists" && m.whitelist);
    assert.equal(saved.length, 0, `«${junk}» не должен сохраняться`);
    assert.match(ui.el("#sitesHint").textContent, /не похоже на адрес сайта/,
      `на «${junk}» нужен понятный отказ`);
    assert.equal(ui.el("#sitesHint").classList.contains("err"), true);
  }
});

test("несколько доменов из одной строки добавляются все", async () => {
  const ui = await boot();
  await renderWith(ui, makeState());
  ui.el("#siteInput").value = "a.example, b.example; c.example\nd.example";
  await click(ui, ui.el("#btnAddSite"));
  const saved = ui.sent.filter((m) => m.cmd === "saveLists").pop();
  // массивы приходят из другого vm-контекста: сравниваем по значению
  assert.deepEqual([...saved.whitelist], ["a.example", "b.example", "c.example", "d.example"]);
  assert.match(ui.el("#sitesHint").textContent, /добавлено/);
});

test("в поле ключа объясняют по-русски, а не «String contains an invalid character»", async () => {
  const ui = await boot();
  for (const bad of ["https://youtube.com/watch?v=x", "vpn://AAAA", "просто текст"]) {
    await assert.rejects(
      async () => ui.ctx.awgbvDecodeVpn(bad),
      (err) => {
        assert.doesNotMatch(String(err.message), /invalid character|zlib|Cannot read/i,
          `для «${bad}» текст должен быть человеческим, получено: ${err.message}`);
        assert.match(String(err.message), /[а-яё]/i);
        return true;
      },
    );
  }
});

test("пустое поле ключа объясняет, что вставить", async () => {
  const ui = await boot();
  await assert.rejects(async () => ui.ctx.awgbvDecodeVpn(""), /vpn:\/\//);
});

test("во время операции кнопка не молчит, а объясняет, почему ничего не произошло", async () => {
  const ui = await boot();
  await renderWith(ui, makeState({ peerBusy: true }));

  const connect = ui.el("#btnAdminConnect");
  assert.equal(connect.getAttribute("aria-disabled"), "true", "кнопка помечена занятой");
  assert.equal(connect.disabled, false, "но нажать её можно — иначе человек не поймёт, что происходит");

  ui.el("#admHost").value = "";
  await click(ui, connect);
  assert.match(ui.el("#admConnectHint").textContent, /идёт другая операция/);
  assert.equal(ui.el("#admConnectHint").classList.contains("warn"), true);
  assert.equal(ui.sent.filter((m) => m.cmd === "bootstrapServer").length, 0,
    "вторая операция не должна уходить в хелпер");
});

test("пустой адрес в форме подключения — сообщение про адрес, а не про пароль", async () => {
  const ui = await boot();
  await renderWith(ui, makeState({ peerBusy: false, servers: [] }));
  ui.el("#admHost").value = "   ";
  ui.el("#admPassword").value = "";
  await click(ui, ui.el("#btnAdminConnect"));
  assert.match(ui.el("#admConnectHint").textContent, /адрес/i);
  assert.equal(ui.sent.filter((m) => m.cmd === "bootstrapServer").length, 0);
});

test("адрес с https:// и портом в строке уходит в хелпер без мусора", async () => {
  const ui = await boot();
  await renderWith(ui, makeState({ servers: [], peerBusy: false }));
  ui.el("#admHost").value = "  https://vpn.example.com/  ";
  ui.el("#admPort").value = "";
  ui.el("#admPassword").value = "секрет";
  await click(ui, ui.el("#btnAdminConnect"));
  const msg = ui.sent.filter((m) => m.cmd === "bootstrapServer").pop();
  assert.ok(msg, "запрос должен уйти");
  assert.equal(msg.host, "vpn.example.com", "схему и слэш надо снять");
  assert.equal(msg.port, 22, "пустой порт — это 22");
});

test("порт, набранный в строке адреса, переезжает в поле порта", async () => {
  const ui = await boot();
  await renderWith(ui, makeState({ servers: [], peerBusy: false }));
  ui.el("#admHost").value = "vpn.example.com:2222";
  ui.el("#admPort").value = "";
  ui.el("#admPassword").value = "секрет";
  await click(ui, ui.el("#btnAdminConnect"));
  const msg = ui.sent.filter((m) => m.cmd === "bootstrapServer").pop();
  assert.equal(msg.host, "vpn.example.com");
  assert.equal(msg.port, 2222, "порт из строки не должен теряться");
  assert.equal(ui.el("#admPort").value, "2222", "человек должен увидеть, что порт распознан");
});

/* ---------------------------------------- первый запуск: понятно ли, что делать */

test("первый запуск: главная не обещает подключение, а показывает, что нажать", async () => {
  const ui = await boot();
  await renderWith(ui, makeState({ profiles: [], activeProfile: null, enabled: false }));

  assert.equal(ui.el("#btnFirstKey").hidden, false, "нужна явная кнопка «Добавить ключ vpn://»");
  assert.doesNotMatch(ui.el("#heroSub").textContent, /нажмите, чтобы подключиться/,
    "без профиля нельзя предлагать «нажмите, чтобы подключиться»");
  assert.match(ui.el("#heroSub").textContent, /профиля нет/i);
  assert.equal(ui.el("#btnReconnect").classList.contains("hidden"), true,
    "«переподключиться» без сервера — ложная надежда");
  assert.equal(ui.el("#firstSteps").classList.contains("hidden"), false,
    "блок «С чего начать» должен быть виден");
});

test("первый запуск: «Добавить ключ vpn://» ведёт на «Серверы»", async () => {
  const ui = await boot();
  await renderWith(ui, makeState({ profiles: [], enabled: false }));
  ui.ctx.showScreen("home");
  await click(ui, ui.el("#btnFirstKey"));
  assert.equal(ctxVar(ui, "currentScreen"), "servers");
});

test("когда профиль есть, подсказки первого запуска убираются", async () => {
  const ui = await boot();
  await renderWith(ui, makeState({
    profiles: [{ id: "p1", name: "Дом", endpoint: "1.2.3.4:1234" }],
    activeProfile: { id: "p1", name: "Дом", endpoint: "1.2.3.4:1234" },
    activeProfileId: "p1",
  }));
  assert.equal(ui.el("#btnFirstKey").hidden, true);
  assert.equal(ui.el("#firstSteps").classList.contains("hidden"), true);
});

test("подсказка про списки не противоречит бейджy режима", async () => {
  const ui = await boot();
  await renderWith(ui, makeState({ whitelist: [], blacklist: [] }));
  ui.ctx.showScreen("sites");
  assert.match(ui.el("#modeBadge").textContent, /весь трафик через VPN/);
  assert.match(ui.el("#sitesHint").textContent, /весь трафик/,
    "при пустом списке «Через VPN» идёт весь трафик — так и надо написать");

  await renderWith(ui, makeState({ whitelist: ["youtube.com"], blacklist: [] }));
  ui.ctx.showScreen("sites");
  assert.match(ui.el("#modeBadge").textContent, /только сайты из списка/);
  assert.match(ui.el("#sitesHint").textContent, /только сайты из этого списка/i);
});

test("на «Панели» видно, что раздел необязательный, и есть выход к ключу", async () => {
  const ui = await boot();
  await renderWith(ui, makeState({ servers: [], profiles: [] }));
  ui.ctx.showScreen("admin");
  const noServer = ui.el("#btnNoServer");
  assert.ok(noServer.handlers.click && noServer.handlers.click.length,
    "кнопка «у меня нет своего сервера» должна работать");
  await click(ui, noServer);
  assert.equal(ctxVar(ui, "currentScreen"), "servers");
});
