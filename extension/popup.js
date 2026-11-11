/* Amnezia Browser VPN — интерфейс попапа.
 *
 * Один экран-приложение: четыре раздела (Главная / Серверы / Сайты / Настройки),
 * нижняя навигация, палитра из AmneziaStyle.qml.
 */

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));

let state = null;
let currentTabId = null;
let currentHost = "";
let activeList = "whitelist"; // какой список открыт на экране «Сайты»
let currentScreen = "home";

/* ------------------------------------------------------------- утилиты */

function fmtBytes(n) {
  if (!n) return "0 Б";
  const units = ["Б", "КБ", "МБ", "ГБ"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i ? 1 : 0)} ${units[i]}`;
}

function fmtUptime(sec) {
  if (!sec) return "—";
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = sec % 60;
  return h ? `${h}ч ${m}м` : m ? `${m}м ${s}с` : `${s}с`;
}

function fmtAgo(unix) {
  if (!unix) return "не было";
  const diff = Math.max(0, Math.floor(Date.now() / 1000 - unix));
  if (diff < 60) return `${diff} с назад`;
  if (diff < 3600) return `${Math.floor(diff / 60)} мин назад`;
  return `${Math.floor(diff / 3600)} ч назад`;
}

async function send(cmd, extra = {}) {
  try {
    return await browser.runtime.sendMessage({ cmd, ...extra });
  } catch (err) {
    return { ok: false, error: String((err && err.message) || err) };
  }
}

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text != null) node.textContent = text;
  return node;
}

/* --------------------------------------------------------- состояние UI */

function tunnelBadge(t) {
  if (!t || !t.running) return { text: "отключено", cls: "" };
  if (t.state === "connected") return { text: "подключено", cls: "ok" };
  if (t.state === "connecting") return { text: "подключение…", cls: "warn" };
  if (t.state === "error") return { text: "ошибка", cls: "err" };
  return { text: t.state || "—", cls: "" };
}

function effectiveMode(s) {
  return (s.whitelist && s.whitelist.length) ? "whitelist" : "blacklist";
}

/* ------------------------------------------------------------- рендер */

function renderTop() {
  const s = state;
  const badge = $("#topBadge");
  const b = tunnelBadge(s.tunnel);
  badge.className = "badge " + b.cls;
  badge.textContent = s.enabled ? b.text : "выкл";
}

function renderHome() {
  const s = state;
  const t = s.tunnel || {};
  const connected = s.enabled && t.state === "connected";
  // две версии знака: цветная (активно) и серая (покой), см. icons/hero-on|off.png
  $("#heroImg").src = connected ? "icons/hero-on.png" : "icons/hero-off.png";
  const btn = $("#heroBtn");
  btn.className = "hero-btn";
  if (s.enabled && t.state === "connected") btn.classList.add("on");
  else if (s.enabled && t.state === "connecting") btn.classList.add("busy");
  else if (t.state === "error" || (s.enabled && !t.running)) btn.classList.add("error");

  const label = $("#heroLabel");
  if (!s.enabled) {
    label.textContent = "Отключено";
  } else if (t.state === "connected") {
    label.textContent = "Защищено";
  } else if (t.state === "connecting") {
    label.textContent = "Подключение…";
  } else if (t.state === "error") {
    label.textContent = "Ошибка";
  } else {
    label.textContent = "Не подключено";
  }

  const sub = $("#heroSub");
  if (s.enabled && t.socksPort) sub.textContent = `SOCKS5 127.0.0.1:${t.socksPort}`;
  else if (s.enabled) sub.textContent = "хелпер не отвечает";
  else sub.textContent = "нажмите, чтобы подключиться";

  $("#statRx").textContent = t.running ? fmtBytes(t.rxBytes) : "—";
  $("#statTx").textContent = t.running ? fmtBytes(t.txBytes) : "—";
  $("#statUp").textContent = t.running ? fmtUptime(t.uptimeSec) : "—";

  const line = $("#serverLine");
  const meta = $("#serverMeta");
  if (s.activeProfile) {
    line.textContent = s.activeProfile.name || s.activeProfile.endpoint;
    const bits = [s.activeProfile.endpoint];
    if (t.lastHandshakeUnix) bits.push("handshake " + fmtAgo(t.lastHandshakeUnix));
    else if (t.running) bits.push("handshake: не было");
    meta.textContent = bits.join(" · ");
  } else {
    line.textContent = "не выбран";
    meta.textContent = "добавьте ключ vpn:// на вкладке «Серверы»";
  }
  $("#btnReconnect").disabled = !s.activeProfile;

  // алерт: отдельно «хелпер не установлен» (это не ошибка, а первый запуск)
  const alertBox = $("#alertBox");
  const alertText = $("#alertText");
  const installBtn = $("#alertInstall");
  if (s.helperMissing) {
    alertText.textContent = "Хелпер awgbv не установлен — без него туннель не поднять.";
    installBtn.classList.remove("hidden");
    alertBox.classList.remove("hidden");
  } else if (s.lastError) {
    alertText.textContent = s.lastError;
    installBtn.classList.add("hidden");
    alertBox.classList.remove("hidden");
  } else if (!s.profiles.length) {
    alertText.textContent = "Профиля нет: на вкладке «Серверы» вставьте ссылку vpn:// из приложения Amnezia.";
    installBtn.classList.add("hidden");
    alertBox.classList.remove("hidden");
  } else {
    alertBox.classList.add("hidden");
  }

  // «этот сайт»
  $("#siteHost").textContent = currentHost || "—";
  $("#siteHint").textContent = currentHost
    ? (() => {
        const inWl = s.whitelist.includes(currentHost);
        const inBl = s.blacklist.includes(currentHost);
        if (inWl) return "сайт в списке «Через VPN»";
        if (inBl) return "сайт в списке «Напрямую»";
        return effectiveMode(s) === "whitelist"
          ? "по общим правилам: этот сайт идёт напрямую"
          : "по общим правилам: этот сайт идёт через VPN";
      })()
    : "эта страница не подходит для правила";
}

function renderServers() {
  const s = state;
  const box = $("#serverList");
  box.innerHTML = "";
  if (!s.profiles.length) {
    box.appendChild(el("p", "hint", "Пока ни одного подключения."));
    return;
  }
  for (const p of s.profiles) {
    const card = el("div", "server" + (p.id === s.activeProfileId ? " selected" : ""));
    card.appendChild(el("div", "dot"));
    const body = el("div", "body");
    body.appendChild(el("div", "name", p.name || p.endpoint));
    const metaBits = [p.endpoint, p.clientIp, p.container].filter(Boolean);
    body.appendChild(el("div", "meta", metaBits.join(" · ")));
    card.appendChild(body);
    if (p.id === s.activeProfileId) card.appendChild(el("span", "badge accent", "активен"));
    const del = el("button", "icon-btn", "×");
    del.title = "Удалить профиль";
    del.addEventListener("click", async (ev) => {
      ev.stopPropagation();
      if (!confirm(`Удалить профиль «${p.name || p.endpoint}»?`)) return;
      await send("removeProfile", { id: p.id });
      await refresh();
    });
    card.appendChild(del);
    card.addEventListener("click", async () => {
      if (p.id === state.activeProfileId) return;
      await send("selectProfile", { id: p.id });
      await refresh();
    });
    box.appendChild(card);
  }
}

function renderSites() {
  const s = state;
  const list = activeList === "whitelist" ? s.whitelist : s.blacklist;

  $("#cntWl").textContent = s.whitelist.length;
  $("#cntBl").textContent = s.blacklist.length;
  for (const b of $$("#sitesTabs button")) {
    b.classList.toggle("active", b.dataset.list === activeList);
  }

  const mode = effectiveMode(s);
  const badge = $("#modeBadge");
  badge.textContent = mode === "whitelist" ? "режим: только список" : "режим: всё кроме списка";
  badge.className = "badge " + (mode === "whitelist" ? "accent" : "");

  $("#sitesHint").textContent = activeList === "whitelist"
    ? "Только сайты из этого списка пойдут через VPN. Пока список пуст — через VPN идёт всё, кроме «Напрямую»."
    : "Эти адреса всегда идут напрямую, мимо туннеля. Приоритет выше, чем у списка «Через VPN».";

  const box = $("#sitesList");
  box.innerHTML = "";
  if (!list.length) {
    box.appendChild(el("p", "hint", "Список пуст."));
  } else {
    for (const host of list) {
      const item = el("div", "list-item");
      item.appendChild(el("span", "host mono", host));
      const del = el("button", "icon-btn", "×");
      del.title = "Удалить";
      del.addEventListener("click", async () => {
        const next = list.filter((h) => h !== host);
        await send("saveLists", activeList === "whitelist" ? { whitelist: next } : { blacklist: next });
        await refresh();
      });
      item.appendChild(del);
      box.appendChild(item);
    }
  }
}

function renderSettings() {
  const s = state;
  const t = s.tunnel || {};
  $("#autoConnect").checked = !!s.autoConnect;
  $("#listenPort").value = s.listenPort || 0;
  $("#helperMissingHint").textContent = s.helperMissing
    ? "Хелпер не найден. Кнопка «Установить хелпер» проведёт по шагам: скачать, открыть, запустить."
    : "";
  $("#hState").textContent = s.helperMissing
    ? "не установлен"
    : (t.running ? (t.state || "—") : "остановлен");
  $("#hVersion").textContent = s.helperVersion || "—";
  $("#hSocks").textContent = t.socksPort ? `127.0.0.1:${t.socksPort}` : "—";
  $("#xVersion").textContent = browser.runtime.getManifest().version;
}

function render() {
  if (!state) return;
  renderTop();
  renderHome();
  renderServers();
  renderSites();
  renderSettings();
}

async function refresh() {
  const res = await send("refresh");
  if (res && res.state) {
    state = res.state;
    render();
  }
}

/* ------------------------------------------------------------ навигация */

function showScreen(name) {
  currentScreen = name;
  for (const section of $$(".screen")) {
    section.classList.toggle("active", section.id === `screen-${name}`);
  }
  for (const btn of $$("#tabbar button")) {
    btn.classList.toggle("active", btn.dataset.screen === name);
  }
  $("#screens").scrollTop = 0;
}

for (const btn of $$("#tabbar button")) {
  btn.addEventListener("click", () => showScreen(btn.dataset.screen));
}

/* --------------------------------------------------------------- Главная */

$("#heroBtn").addEventListener("click", async () => {
  const res = await send("setEnabled", { value: !state.enabled });
  if (res && res.ok === false && res.error) {
    $("#alertBox").textContent = res.error;
    $("#alertBox").classList.remove("hidden");
  }
  await refresh();
});

$("#btnReconnect").addEventListener("click", async () => {
  await send("disconnect");
  await send("setEnabled", { value: true });
  await refresh();
});

for (const btn of $$("#siteSwitch button")) {
  btn.addEventListener("click", async () => {
    if (!currentTabId) return;
    await send("tabOverride", { tabId: currentTabId, value: btn.dataset.value });
    for (const b of $$("#siteSwitch button")) b.classList.toggle("active", b === btn);
  });
}

/* --------------------------------------------------------------- Серверы */

$("#btnAddProfile").addEventListener("click", async () => {
  const link = $("#newLink").value.trim();
  if (!link) return;
  const hint = $("#addHint");
  hint.textContent = "разбираю…";
  const res = await send("addProfile", { link });
  if (res && res.ok) {
    hint.textContent = "добавлено";
    $("#newLink").value = "";
    await refresh();
    showScreen("servers");
  } else {
    hint.textContent = "ошибка: " + ((res && res.error) || "?");
  }
});

/* ----------------------------------------------------------------- Сайты */

for (const btn of $$("#sitesTabs button")) {
  btn.addEventListener("click", () => {
    activeList = btn.dataset.list;
    $("#sitesBulk").value = "";
    renderSites();
  });
}

function normalizeHost(raw) {
  let h = String(raw || "").trim().toLowerCase();
  if (!h) return "";
  h = h.replace(/^https?:\/\//, "").replace(/^\/\//, "");
  h = h.split("/")[0].split("?")[0].split("#")[0];
  h = h.split("@").pop();
  if (h.startsWith("*.")) return h; // wildcard оставляем как есть
  h = h.split(":")[0];
  return h.replace(/\.$/, "");
}

async function addSite(raw) {
  const host = normalizeHost(raw);
  if (!host) return false;
  const s = state;
  const other = activeList === "whitelist" ? s.blacklist : s.whitelist;
  if (other.includes(host)) {
    // один и тот же адрес в двух списках — убираем из противоположного
    await send("saveLists", activeList === "whitelist"
      ? { blacklist: other.filter((h) => h !== host) }
      : { whitelist: other.filter((h) => h !== host) });
    await refresh();
  }
  const list = activeList === "whitelist" ? state.whitelist : state.blacklist;
  if (list.includes(host)) return false;
  const next = [...list, host];
  await send("saveLists", activeList === "whitelist" ? { whitelist: next } : { blacklist: next });
  await refresh();
  return true;
}

$("#btnAddSite").addEventListener("click", async () => {
  const input = $("#siteInput");
  const ok = await addSite(input.value);
  if (ok) input.value = "";
  input.focus();
});

$("#siteInput").addEventListener("keydown", async (ev) => {
  if (ev.key === "Enter") {
    ev.preventDefault();
    const input = $("#siteInput");
    const ok = await addSite(input.value);
    if (ok) input.value = "";
  }
});

$("#btnSaveBulk").addEventListener("click", async () => {
  const hosts = $("#sitesBulk").value
    .split(/[\r\n,;]+/)
    .map(normalizeHost)
    .filter(Boolean);
  const uniq = Array.from(new Set(hosts));
  await send("saveLists", activeList === "whitelist" ? { whitelist: uniq } : { blacklist: uniq });
  $("#bulkHint").textContent = `сохранено: ${uniq.length}`;
  $("#sitesBulk").value = "";
  await refresh();
});

$("#btnClearList").addEventListener("click", async () => {
  if (!confirm("Очистить этот список?")) return;
  await send("saveLists", activeList === "whitelist" ? { whitelist: [] } : { blacklist: [] });
  await refresh();
});

/* ------------------------------------------------------------- Настройки */

$("#btnSavePrefs").addEventListener("click", async () => {
  const res = await send("savePrefs", {
    autoConnect: $("#autoConnect").checked,
    listenPort: Number($("#listenPort").value) || 0,
  });
  $("#prefsHint").textContent = res && res.ok ? "сохранено" : "ошибка: " + ((res && res.error) || "?");
  await refresh();
});

$("#btnRestart").addEventListener("click", async () => {
  await send("disconnect");
  await send("setEnabled", { value: state.enabled });
  await refresh();
});

$("#btnQuit").addEventListener("click", async () => {
  if (!confirm("Остановить хелпер? Туннель закроется, расширение останется.")) return;
  await send("quitHelper");
  await refresh();
});

/* -------------------------------------------------------------- запуск */

/* --------------------------------------------- мастер установки хелпера */

let guide = null;

function renderInstallSheet() {
  const g = state && state.platformOs ? installGuide(state.platformOs) : installGuide("");
  guide = g;

  $("#installIntro").textContent = g.intro;
  const box = $("#installSteps");
  box.innerHTML = "";
  for (const step of g.steps) {
    const li = document.createElement("li");
    li.appendChild(el("span", "step-title", step.title));
    li.appendChild(document.createTextNode(step.body));
    if (step.command) li.appendChild(el("code", null, step.command));
    box.appendChild(li);
  }

  const fb = $("#installFallback");
  fb.textContent = "";
  if (g.fallback && g.fallback.label) {
    fb.appendChild(document.createTextNode(g.fallback.label));
    if (g.command) {
      fb.appendChild(document.createElement("br"));
      fb.appendChild(el("code", null, g.command));
    }
  }

  $("#installDownload").classList.toggle("hidden", !g.files.length);
  $("#installDownload").textContent = g.files.length === 1
    ? "Скачать установщик"
    : `Скачать установщик (${g.files.length} файла)`;
  $("#installCopy").classList.toggle("hidden", !g.command);
  $("#installStatus").textContent = "";
}

function openInstallSheet() {
  if (!state) return;
  renderInstallSheet();
  $("#installSheet").classList.remove("hidden");
}

function closeInstallSheet() {
  $("#installSheet").classList.add("hidden");
}

$("#installClose").addEventListener("click", closeInstallSheet);
$("#installSheet").addEventListener("click", (ev) => {
  if (ev.target === $("#installSheet")) closeInstallSheet();
});

$("#installDownload").addEventListener("click", async () => {
  const box = $("#installStatus");
  if (!guide || !guide.downloadUrls.length) return;
  box.textContent = "скачиваю…";
  try {
    for (const url of guide.downloadUrls) {
      await browser.downloads.download({ url, saveAs: false });
    }
    box.textContent = `${guide.files.length} файл(а) в «Загрузках»`;
  } catch (err) {
    box.textContent = "не удалось скачать: " + ((err && err.message) || err);
  }
});

$("#installCopy").addEventListener("click", async () => {
  const box = $("#installStatus");
  if (!guide || !guide.command) return;
  try {
    await navigator.clipboard.writeText(guide.command);
    box.textContent = "команда скопирована";
  } catch (err) {
    box.textContent = "скопируйте вручную: " + guide.command;
  }
});

$("#installRecheck").addEventListener("click", async () => {
  const box = $("#installStatus");
  box.textContent = "проверяю…";
  const res = await send("recheck");
  if (res && res.state) {
    state = res.state;
    render();
  }
  if (state && !state.helperMissing) {
    box.textContent = "хелпер найден";
    setTimeout(closeInstallSheet, 700);
    if (state.enabled && !state.tunnel.running) await send("connect");
    await refresh();
  } else {
    box.textContent = "пока не найден — проверьте, что установщик отработал, и перезапустите Firefox";
  }
});

$("#alertInstall").addEventListener("click", openInstallSheet);
$("#btnInstall").addEventListener("click", openInstallSheet);

browser.runtime.onMessage.addListener((msg) => {
  if (msg && msg.type === "state" && msg.payload) {
    state = msg.payload;
    render();
  }
});

async function init() {
  const [tab] = await browser.tabs.query({ active: true, currentWindow: true });
  if (tab) {
    currentTabId = tab.id;
    try {
      currentHost = new URL(tab.url).hostname;
    } catch (err) {
      currentHost = "";
    }
  }
  const res = await send("getState");
  if (res && res.state) {
    state = res.state;
    render();
  }
  const overrides = await send("listTabOverrides");
  const map = new Map(overrides && overrides.overrides ? overrides.overrides : []);
  const cur = map.get(currentTabId) || "auto";
  for (const b of $$("#siteSwitch button")) b.classList.toggle("active", b.dataset.value === cur);

  showScreen("home");
  if (state && state.helperMissing) openInstallSheet();
  refresh();
  setInterval(refresh, 4000);
}

init();
