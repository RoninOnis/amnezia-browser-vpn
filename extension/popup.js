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

  const hasProfile = !!(s.profiles && s.profiles.length);
  // круг без подписи не читается как кнопка: на первом запуске даём обычную кнопку
  const firstKey = $("#btnFirstKey");
  if (firstKey) firstKey.hidden = hasProfile;
  const sub = $("#heroSub");
  if (!hasProfile) {
    // «нажмите, чтобы подключиться» без профиля — обещание, которого не будет
    sub.textContent = "профиля нет — добавьте ключ на вкладке «Серверы»";
  } else if (s.enabled && t.socksPort) sub.textContent = `SOCKS5 127.0.0.1:${t.socksPort}`;
  else if (s.enabled) sub.textContent = "помощник не отвечает";
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
  // «переподключиться» без сервера только путает: нечего переподключать
  $("#btnReconnect").classList.toggle("hidden", !s.activeProfile);

  // алерт: отдельно «хелпер не установлен» (это не ошибка, а первый запуск)
  const alertBox = $("#alertBox");
  const alertText = $("#alertText");
  const installBtn = $("#alertInstall");
  if (s.helperMissing) {
    alertText.textContent = "Помощник не установлен — без него туннель не поднять.";
    installBtn.classList.remove("hidden");
    alertBox.classList.remove("hidden");
  } else if (s.lastError) {
    alertText.textContent = s.lastError;
    installBtn.classList.add("hidden");
    alertBox.classList.remove("hidden");
  } else if (!s.profiles.length) {
    alertText.textContent = "Профиля нет: на вкладке «Серверы» вставьте ссылку vpn:// и нажмите «Добавить профиль».";
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
  const first = $("#firstSteps");
  if (first) first.classList.toggle("hidden", !!(state.profiles || []).length);
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

// Сообщение о добавлении сайта живёт несколько секунд: опрос перерисовывает
// экран каждые 4 с, и без этого подсказка исчезала раньше, чем её прочли.
let sitesMsg = "";
let sitesMsgAt = 0;
let sitesMsgKind = "";

function siteMsg(text, kind) {
  sitesMsg = text;
  sitesMsgKind = kind || "";
  sitesMsgAt = Date.now();
  const box = $("#sitesHint");
  if (box) {
    box.textContent = text;
    box.className = "hint" + (kind ? " " + kind : "");
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
  badge.textContent = mode === "whitelist"
    ? "сейчас: только сайты из списка"
    : "сейчас: весь трафик через VPN";
  badge.className = "badge " + (mode === "whitelist" ? "accent" : "");

  if (sitesMsg && Date.now() - sitesMsgAt < 6000) {
    $("#sitesHint").textContent = sitesMsg;
    $("#sitesHint").className = "hint" + (sitesMsgKind ? " " + sitesMsgKind : "");
  } else {
    // подсказка обязана совпадать с бейджем режима: раньше «режим: всё кроме списка»
    // стоял рядом с «только сайты из этого списка пойдут через VPN» — два
    // взаимоисключающих утверждения на одном экране
    const wlEmpty = !(s.whitelist || []).length;
    if (activeList === "whitelist") {
      $("#sitesHint").textContent = wlEmpty
        ? "Список пуст, поэтому через VPN идёт весь трафик. Добавьте сайты сюда — и через VPN пойдут только они."
        : "Через VPN идут только сайты из этого списка.";
    } else {
      $("#sitesHint").textContent = (s.blacklist || []).length
        ? "Эти сайты всегда идут напрямую, мимо туннеля — даже если попали в список «Через VPN»."
        : "Здесь сайты, которые всегда идут напрямую, мимо туннеля. Пока пусто — таких сайтов нет.";
    }
    $("#sitesHint").className = "hint";
  }

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

// Переключатель нельзя перерисовывать, пока пользователь только что его нажал:
// попап обновляет состояние каждые 4 секунды, и «откат» выглядел как поломка
// ползунка — нажал ВЫКЛ, а он вернулся в ВКЛ.
const justTouched = new Map();

function setSwitch(el, value) {
  const at = justTouched.get(el.id) || 0;
  if (Date.now() - at < 2500) return; // не затираем только что сделанный выбор
  el.checked = value;
}

// Переключатели сохраняются сразу при клике: отдельная кнопка «Сохранить»
// для них не нужна и только путала.
function bindSwitch(el, key) {
  el.addEventListener("change", async () => {
    justTouched.set(el.id, Date.now());
    const res = await send("savePrefs", { [key]: el.checked });
    if (!res || !res.ok) {
      el.checked = !el.checked; // откат: сохранить не удалось
      $("#prefsHint").textContent = "не сохранено: " + ((res && res.error) || "нет ответа");
      return;
    }
    $("#prefsHint").textContent = "";
    if (res.state) {
      state = res.state;
      renderTop();
      renderHome();
    }
  });
}

function renderSettings() {
  const s = state;
  const t = s.tunnel || {};
  setSwitch($("#autoConnect"), !!s.autoConnect);
  setSwitch($("#mtuAuto"), s.mtuAuto !== false);
  $("#listenPort").value = s.listenPort || 0;
  const installBtn = $("#btnInstall");
  if (installBtn) {
    installBtn.textContent = s.helperMissing ? "Установить помощника" : "Переустановить помощника";
  }
  $("#helperMissingHint").textContent = s.helperMissing
    ? "Помощник не найден. Кнопка «Установить помощник» проведёт по шагам: скачать, открыть, запустить."
    : "";
  $("#hState").textContent = s.helperMissing
    ? "не установлен"
    : (t.running ? (t.state || "—") : "остановлен");
  $("#hVersion").textContent = s.helperVersion || "—";
  $("#hSocks").textContent = t.socksPort ? `127.0.0.1:${t.socksPort}` : "—";
  $("#hMtu").textContent = t.mtu
    ? `${t.mtu}${t.mtuAuto ? " (подобран под сеть)" : " (из профиля)"}${t.relay ? ", через релей" : ""}`
    : "—";
  const att = s.lastAttempt;
  $("#hAttempt").textContent = !att
    ? "—"
    : (att.ok ? "успешно, " : "ошибка, ") + fmtAgo(Math.floor(att.at / 1000))
      + (att.ok ? "" : " · " + (att.error || "без описания"));
  $("#hLogPath").textContent = logPathFor(s.platformOs);
  $("#xVersion").textContent = browser.runtime.getManifest().version;
  renderUpdates();
}

// Единая строка статуса версии: «0.3.7 · актуально», «0.3.6 → 0.3.7» или
// честное «не удалось проверить». Зелёного «всё хорошо» без данных не бывает.
function versionCell(el, part) {
  if (!part) {
    el.textContent = "—";
    el.className = "";
    return;
  }
  if (part.missing) {
    el.textContent = "не установлен";
    el.className = "warn";
    return;
  }
  if (part.unknown) {
    el.textContent = part.current ? `${part.current} · не удалось проверить` : "не удалось проверить";
    el.className = "warn";
    return;
  }
  if (part.outdated) {
    el.textContent = `${part.current || "?"} → ${part.latest}`;
    el.className = "warn";
    return;
  }
  el.textContent = `${part.current} · актуально`;
  el.className = "";
}

// Показываем только путь той системы, где стоит расширение: чужие пути — шум.
function logPathFor(platformOs) {
  const win = platformOs === "win";
  return win
    ? "%LOCALAPPDATA%\\AwgBrowserVpn\\helper.log"
    : "~/.local/state/awgbv/helper.log";
}

function renderUpdates() {
  const u = state && state.updates;
  const addonEl = $("#updAddon");
  const helperEl = $("#updHelper");
  const hint = $("#updHint");
  const dl = $("#btnDownloadAddon");
  const upd = $("#btnUpdateHelper");
  if (upd) {
    // пока проверки версий не было, u === null: без защиты рендер падал на первом
    // же вызове и «Настройки» не обновлялись до конца сессии
    const helperPart = u && u.helper;
    upd.hidden = !(helperPart && (helperPart.outdated || helperPart.missing));
    upd.disabled = !!(state && state.updatingHelper);
    upd.textContent = state && state.updatingHelper ? "Обновляю…" : "Обновить помощника";
  }
  if (!u) {
    addonEl.textContent = "не проверялось";
    addonEl.className = "";
    helperEl.textContent = state && state.helperVersion ? `${state.helperVersion} · не проверялось` : "—";
    helperEl.className = "";
    hint.textContent = "Проверка идёт через помощника: отдельного доступа к сети расширению не нужно.";
    dl.hidden = true;
    return;
  }
  versionCell(addonEl, u.addon);
  versionCell(helperEl, u.helper);

  const lines = [];
  if (u.error) lines.push(`Проверить не удалось: ${u.error}.`);
  if (u.helper && u.helper.missing && !u.error) lines.push("Помощник не установлен — сравнивать нечего.");
  if (u.addon && u.addon.outdated) lines.push("Аддон старее релиза: обновите .xpi из релиза.");
  if (u.helper && u.helper.outdated) lines.push("Помощник старее релиза — нажмите «Обновить помощника»: туннель перезапустится сам.");
  const last = state && state.lastHelperUpdate;
  if (last) {
    lines.length = 0;
    lines.push(last.ok
      ? `Помощник обновлён${last.from ? ` с ${last.from}` : ""} до ${last.to}, туннель перезапущен.`
      : `Обновить помощника не удалось: ${last.error || "неизвестная ошибка"}.`);
  }
  if (!lines.length) lines.push("Всё актуально.");
  hint.textContent = lines.join(" ");
  dl.hidden = !(u.addon && u.addon.outdated && u.pageUrl);
}

// Подсказки: серый .hint — это справка, ошибки и предупреждения красятся.
function setHint(selector, text, kind) {
  const node = $(selector);
  if (!node) return;
  if (node.textContent !== text) node.textContent = text;
  node.classList.toggle("err", kind === "err");
  node.classList.toggle("warn", kind === "warn");
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

$("#btnFirstKey").addEventListener("click", () => {
  showScreen("servers");
  const box = $("#newLink");
  if (box) {
    box.focus();
    box.scrollIntoView({ block: "center" });
  }
});

$("#heroBtn").addEventListener("click", async () => {
  if (!(state.profiles || []).length) {
    showScreen("servers");
    const box = $("#newLink");
    if (box) {
      box.focus();
      box.scrollIntoView({ block: "center" });
    }
    setHint("#addHint", "вставьте сюда ключ vpn:// — он начинается с vpn://", "warn");
    return;
  }
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
  setHint("#addHint", "разбираю…");
  const res = await send("addProfile", { link });
  if (res && res.ok) {
    setHint("#addHint", res.duplicate ? "такой ключ уже добавлен" : "добавлено");
    $("#newLink").value = "";
    await refresh();
    showScreen("servers");
  } else {
    setHint("#addHint", "ошибка: " + ((res && res.error) || "?"), "err");
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

// Приводит введённый адрес к домену. Возвращает "" если это не похоже на сайт:
// люди вставляют «https://youtube.com/watch?v=…», «YOUTUBE.COM», «*.example.com»,
// «яндекс.рф», а иногда просто текст («два через запятую»), и молча сохранять
// такой текст в список — значит обещать фильтрацию, которой не будет.
function normalizeHost(raw) {
  let h = String(raw || "").trim().toLowerCase();
  if (!h) return "";
  h = h.replace(/^[a-z]+:\/\//, "").replace(/^\/\//, "");
  h = h.split("/")[0].split("?")[0].split("#")[0];
  h = h.split("@").pop();
  h = h.split(":")[0].replace(/\.$/, "");

  let wildcard = false;
  if (h.startsWith("*.")) {
    wildcard = true;
    h = h.slice(2);
  }
  if (!h) return "";
  // кириллические домены приводим к тому виду, который приходит в запросах
  if (/[^\x00-\x7f]/.test(h)) {
    try {
      h = new URL("http://" + h).hostname;
    } catch (err) {
      return "";
    }
  }
  if (h === "localhost") return h;
  // домен: хотя бы одна точка, метки из букв/цифр/дефисов, TLD не из дефисов
  if (!/^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/.test(h)) {
    if (!/^\d{1,3}(\.\d{1,3}){3}$/.test(h)) return "";
  }
  return wildcard ? "*." + h : h;
}

// Пользователь часто вставляет несколько адресов сразу: «youtube.com, vk.com»
// или списком из мессенджера. Разбираем по запятым/пробелам/переносам, а мусор
// показываем, а не сохраняем молча.
function splitSites(raw) {
  return String(raw || "").split(/[\s,;]+/).map((s) => s.trim()).filter(Boolean);
}

async function addSite(raw) {
  const parts = splitSites(raw);
  if (!parts.length) return false;
  if (parts.length > 1) {
    let added = 0;
    const rejected = [];
    for (const part of parts) {
      if (await addOneSite(part)) added += 1;
      else if (!normalizeHost(part)) rejected.push(part);
    }
    if (rejected.length) {
      siteMsg("не похоже на адрес сайта: " + rejected.join(", "), "err");
    } else if (added) {
      siteMsg(`добавлено сайтов: ${added}`);
    }
    return added > 0;
  }
  return addOneSite(parts[0]);
}

async function addOneSite(raw) {
  const host = normalizeHost(raw);
  if (!host) {
    siteMsg(`«${String(raw || "").trim()}» не похоже на адрес сайта — нужен домен вида example.com`, "err");
    return false;
  }
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

bindSwitch($("#autoConnect"), "autoConnect");
bindSwitch($("#mtuAuto"), "mtuAuto");

// Кнопка осталась для поля порта: переключатели сохраняются сами.
$("#btnSavePrefs").addEventListener("click", async () => {
  const res = await send("savePrefs", { listenPort: Number($("#listenPort").value) || 0 });
  $("#prefsHint").textContent = res && res.ok ? "сохранено" : "ошибка: " + ((res && res.error) || "?");
  await refresh();
});

$("#btnRestart").addEventListener("click", async () => {
  await send("disconnect");
  await send("setEnabled", { value: state.enabled });
  await refresh();
});

$("#btnQuit").addEventListener("click", async () => {
  if (!confirm("Остановить помощника? Туннель закроется, расширение останется.")) return;
  await send("quitHelper");
  await refresh();
});

/* -------------------------------------------------------------- запуск */

/* --------------------------------------------- мастер установки помощника */

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
    box.textContent = "помощник найден";
    setTimeout(closeInstallSheet, 700);
    if (state.enabled && !state.tunnel.running) await send("connect");
    await refresh();
  } else {
    box.textContent = "пока не найден — проверьте, что установщик отработал, и перезапустите Firefox";
  }
});

$("#alertInstall").addEventListener("click", openInstallSheet);
$("#btnInstall").addEventListener("click", openInstallSheet);

$("#btnCheckUpdates").addEventListener("click", async () => {
  const btn = $("#btnCheckUpdates");
  btn.disabled = true;
  btn.textContent = "Проверяю…";
  try {
    const res = await send("checkVersions", { force: true });
    if (res && res.state) {
      state = res.state;
      render();
    }
    if (res && !res.ok) $("#updHint").textContent = "Проверить не удалось: " + (res.error || "нет ответа");
  } finally {
    btn.disabled = false;
    btn.textContent = "Проверить";
  }
});

$("#btnUpdateHelper").addEventListener("click", async () => {
  const btn = $("#btnUpdateHelper");
  btn.disabled = true;
  btn.textContent = "Обновляю…";
  $("#updHint").textContent = "Скачиваю и проверяю нового помощника…";
  try {
    const res = await send("updateHelper");
    if (res && res.state) {
      state = res.state;
      render();
    }
    if (res && !res.ok) {
      $("#updHint").textContent = "Обновить не удалось: " + (res.error || "нет ответа");
    }
  } finally {
    btn.disabled = false;
    btn.textContent = "Обновить помощника";
  }
});

$("#btnDownloadAddon").addEventListener("click", () => {
  const u = state && state.updates;
  const url = (u && u.pageUrl) || "https://github.com/RoninOnis/amnezia-browser-vpn/releases/latest";
  browser.tabs.create({ url });
});

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
      const u = new URL(tab.url);
      // только настоящие сайты: у служебных страниц браузера и у страниц самого
      // расширения hostname — UUID вида e5fc0d4d-…, и человек видел его в карточке
      currentHost = /^https?:$/.test(u.protocol) && /\.[a-z0-9-]+$/i.test(u.hostname)
        ? u.hostname : "";
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
