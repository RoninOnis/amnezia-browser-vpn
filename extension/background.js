/* Amnezia Browser VPN — background (Firefox MV3 event page).
 *
 * Responsibilities:
 *  1. keep an in-memory mirror of the settings so proxy.onRequest can answer
 *     synchronously (the API does not allow async decisions);
 *  2. decide per-request whether traffic goes through the local AWG SOCKS5
 *     tunnel or straight out (that is the split tunnelling);
 *  3. talk to the native helper over native messaging.
 */

const NATIVE_HOST = "awgbv";
const STATE_KEY = "settings";
const POLL_ALARM = "awgbv-poll";
const LOOPBACK = /^(localhost|127\.\d+\.\d+\.\d+|::1|\[::1\]|.*\.local|.*\.localhost)$/i;

const SETTINGS_VERSION = 2;

const DEFAULTS = {
  enabled: false,
  // Подбор MTU: корпоративные туннели (AnyConnect держит 1400) режут наши пакеты,
  // поэтому при отсутствии handshake хелпер спускается вниз 1376 -> 1280 -> 1240 -> 1200.
  mtuAuto: true,
  mtu: 0,
  // Автостарт включён: «включено» = «хочу, чтобы работало всегда». Пока значение было false
  // по умолчанию, после перезапуска Firefox туннель никто не поднимал — жалоба «не стартует».
  autoConnect: true,
  // Списки как в Amnezia: «Через VPN» и «Напрямую». Отдельного переключателя режима
  // нет — режим выводится из того, пуст ли список «Через VPN» (см. effectiveMode).
  whitelist: [],
  blacklist: [],
  profiles: [],
  activeProfileId: null,
  listenPort: 0, // 0 = эфемерный порт, назначает хелпер
};

const state = {
  settings: { ...DEFAULTS },
  tunnel: { running: false, state: "stopped", socksPort: 0, error: "" },
  helperVersion: "",
  tabOverride: new Map(), // tabId -> "auto" | "tunnel" | "direct"
  nativePort: null,
  nativeSeq: 0,
  pending: new Map(),
  lastError: "",
  // «No such native application awgbv» — не сбой связи, а отсутствие установленного хелпера:
  // это единственный случай, когда показываем мастер установки.
  helperMissing: false,
  platformOs: "",
  // сторож автостарта: попытка, отсрочка и нарастающая пауза между переподключениями
  lastAttempt: null,
  retryDelay: 15000,
  nextRetryAt: 0,
  connecting: false, // идёт connect: второй параллельно не запускаем
  updates: null,      // результат проверки версий аддона и хелпера
  updatingHelper: false, // идёт обновление хелпера
  lastHelperUpdate: null, // {at, ok, from, to, error}
};

const HOST_MISSING = /no such native application|native application .* not found|No such native/i;

function noteHelperError(message) {
  state.lastError = message || "";
  if (HOST_MISSING.test(state.lastError)) state.helperMissing = true;
}

/* ---------------------------------------------------------------- settings */

async function loadSettings() {
  const stored = await browser.storage.local.get(STATE_KEY);
  const saved = stored[STATE_KEY] || {};
  const merged = { ...DEFAULTS, ...saved };
  if ((saved.settingsVersion || 0) < SETTINGS_VERSION) {
    // миграция: до 0.3.0 автостарт был выключен по умолчанию, из-за чего VPN не поднимался
    // после перезапуска браузера. Уважаем выбор пользователя дальше, но один раз включаем.
    merged.autoConnect = true;
    merged.settingsVersion = SETTINGS_VERSION;
    await browser.storage.local.set({ [STATE_KEY]: merged });
  }
  state.settings = merged;
  return state.settings;
}

async function saveSettings(patch = {}) {
  state.settings = { ...state.settings, ...patch };
  await browser.storage.local.set({ [STATE_KEY]: state.settings });
  updateBadge();
  return state.settings;
}

function activeProfile() {
  const s = state.settings;
  return s.profiles.find((p) => p.id === s.activeProfileId) || null;
}

/* ------------------------------------------------------------ native helper */

function onNativeMessage(msg) {
  state.helperMissing = false;
  const id = msg && msg.id;
  if (id != null && state.pending.has(id)) {
    const { resolve, timer } = state.pending.get(id);
    clearTimeout(timer);
    state.pending.delete(id);
    resolve(msg);
    return;
  }
  if (msg && msg.event === "status" && msg.status) {
    applyTunnelStatus(msg.status);
  }
}

function ensurePort() {
  if (state.nativePort) return state.nativePort;
  try {
    const port = browser.runtime.connectNative(NATIVE_HOST);
    port.onMessage.addListener(onNativeMessage);
    port.onDisconnect.addListener((p) => {
      const err = browser.runtime.lastError;
      noteHelperError((err && err.message) || (p && p.error && p.error.message) || "native host disconnected");
      state.nativePort = null;
      for (const [, { resolve, timer }] of state.pending) {
        clearTimeout(timer);
        resolve({ ok: false, error: state.lastError });
      }
      state.pending.clear();
      updateBadge();
    });
    state.nativePort = port;
    state.lastError = "";
    return port;
  } catch (err) {
    noteHelperError(String((err && err.message) || err));
    state.nativePort = null;
    return null;
  }
}

function sendNative(cmd, extra = {}, timeoutMs = 30000) {
  const port = ensurePort();
  if (!port) {
    return Promise.resolve({ ok: false, error: state.lastError || "хелпер не найден" });
  }
  const id = ++state.nativeSeq;
  return new Promise((resolve) => {
    const timer = setTimeout(() => {
      state.pending.delete(id);
      resolve({ ok: false, error: "таймаут ответа хелпера" });
    }, timeoutMs);
    state.pending.set(id, { resolve, timer });
    try {
      port.postMessage({ id, cmd, ...extra });
    } catch (err) {
      clearTimeout(timer);
      state.pending.delete(id);
      resolve({ ok: false, error: String((err && err.message) || err) });
    }
  });
}

function applyTunnelStatus(status) {
  if (!status) return;
  state.tunnel = {
    running: !!status.running,
    state: status.state || "stopped",
    socksPort: status.socksPort || 0,
    error: status.error || "",
    profileName: status.profileName || "",
    endpoint: status.endpoint || "",
    lastHandshakeUnix: status.lastHandshakeUnix || 0,
    rxBytes: status.rxBytes || 0,
    txBytes: status.txBytes || 0,
    uptimeSec: status.uptimeSec || 0,
  };
  updateBadge();
  notifyUI();
}

/* --------------------------------------------------------------- proxy core */

function matchesAny(host, list) {
  if (!host || !Array.isArray(list)) return false;
  const h = String(host).toLowerCase().replace(/\.$/, "");
  for (const raw of list) {
    let e = String(raw || "").trim().toLowerCase();
    if (!e || e.startsWith("#")) continue;
    if (e.startsWith("*.")) e = e.slice(2);
    if (e.startsWith(".")) e = e.slice(1);
    e = e.replace(/\.$/, "");
    if (!e) continue;
    if (h === e || h.endsWith("." + e)) return true;
  }
  return false;
}

// Режим выводится из списков, как в Amnezia: непустой список «Через VPN» делает
// поведение белым списком, пустой — чёрным.
function effectiveMode(settings) {
  const s = settings || state.settings;
  return (s.whitelist && s.whitelist.length) ? "whitelist" : "blacklist";
}

function useTunnelFor(host, tabId) {
  const s = state.settings;
  if (!s.enabled || !state.tunnel.running || !state.tunnel.socksPort) return false;
  if (!host || LOOPBACK.test(host)) return false;
  const override = state.tabOverride.get(tabId);
  if (override === "direct") return false;
  if (override === "tunnel") return true;
  // «Напрямую» всегда имеет приоритет над «Через VPN»
  if (matchesAny(host, s.blacklist)) return false;
  if (s.whitelist.length) return matchesAny(host, s.whitelist);
  return true;
}

function socksInfo() {
  return {
    type: "socks",
    host: "127.0.0.1",
    port: state.tunnel.socksPort,
    proxyDNS: true,
    version: 5,
  };
}

function onProxyRequest(details) {
  let host = details.host;
  if (!host && details.url) {
    try {
      host = new URL(details.url).hostname;
    } catch (err) {
      host = "";
    }
  }
  return useTunnelFor(host, details.tabId) ? socksInfo() : { type: "direct" };
}

browser.proxy.onRequest.addListener(onProxyRequest, { urls: ["<all_urls>"] });
browser.proxy.onError.addListener((err) => {
  state.lastError = "proxy: " + ((err && err.message) || err);
  console.error("proxy error", err);
});

/* ------------------------------------------------------------------- badge */

function updateBadge() {
  const s = state.settings;
  let text = "";
  let color = "#666";
  if (s.enabled && state.tunnel.running && state.tunnel.state === "connected") {
    text = "ON";
    color = "#2e7d32";
  } else if (s.enabled && state.tunnel.running) {
    text = "…";
    color = "#f9a825";
  } else if (s.enabled) {
    text = "!";
    color = "#c62828";
  }
  browser.action.setBadgeText({ text }).catch(() => {});
  browser.action.setBadgeBackgroundColor({ color }).catch(() => {});
  browser.action.setTitle({
    title: text === "ON" ? "Amnezia Browser VPN: включён" : "Amnezia Browser VPN",
  }).catch(() => {});
}

function notifyUI() {
  browser.runtime.sendMessage({ type: "state", payload: publicState() }).catch(() => {});
}

function publicState() {
  const s = state.settings;
  const active = activeProfile();
  return {
    enabled: !!s.enabled,
    autoConnect: !!s.autoConnect,
    mode: effectiveMode(s), // выводится из списков, отдельного переключателя нет
    helperVersion: state.helperVersion,
    whitelist: s.whitelist,
    blacklist: s.blacklist,
    profiles: s.profiles.map((p) => ({
      id: p.id,
      name: p.name,
      endpoint: p.endpoint,
      container: p.container,
      clientIp: p.clientIp,
      addedAt: p.addedAt,
    })),
    activeProfileId: s.activeProfileId,
    activeProfile: active ? { id: active.id, name: active.name, endpoint: active.endpoint } : null,
    listenPort: s.listenPort,
    tunnel: state.tunnel,
    lastError: state.lastError,
    helperMissing: state.helperMissing,
    platformOs: state.platformOs,
    lastAttempt: state.lastAttempt,
    updates: state.updates,
    updatingHelper: state.updatingHelper,
    lastHelperUpdate: state.lastHelperUpdate,
  };
}

/* ------------------------------------------------------- проверка версий */

// Разбор версии в числа. Компонентов может быть сколько угодно: 0.3.6.1 законен.
function parseVersion(v) {
  const clean = String(v || "").trim().replace(/^v/i, "").split(/[-+]/)[0];
  if (!clean) return null;
  const parts = clean.split(".");
  const out = [];
  for (const p of parts) {
    if (!/^\d+$/.test(p)) return null;
    out.push(Number(p));
  }
  return out;
}

// -1 / 0 / 1. Хвостовые нули не считаются: 0.3.6 == 0.3.6.0, но 0.3.6.1 > 0.3.6.
function compareVersions(a, b) {
  const pa = parseVersion(a);
  const pb = parseVersion(b);
  if (!pa || !pb) return null;
  const n = Math.max(pa.length, pb.length);
  for (let i = 0; i < n; i++) {
    const x = pa[i] || 0;
    const y = pb[i] || 0;
    if (x !== y) return x < y ? -1 : 1;
  }
  return 0;
}

function isNewer(latest, current) {
  const cmp = compareVersions(latest, current);
  return cmp === 1; // null (непонятная версия) трактуем как «не новее»
}

// Проверка актуальности аддона и хелпера. Делается в хелпере, поэтому новых
// разрешений в манифесте не нужно. Если проверить не удалось — честно говорим
// «не удалось», а не показываем зелёное «всё актуально»: именно из-за такого
// вранья люди месяцами сидят на старом хелпере.
async function checkVersions(force = false) {
  const addonCurrent = browser.runtime.getManifest().version;
  const helperCurrent = state.helperMissing ? "" : state.helperVersion || "";

  let info = null;
  let error = "";
  if (state.helperMissing) {
    error = "хелпер не установлен";
  } else {
    const resp = await sendNative("update_check", { force }, 15000);
    if (resp && resp.ok && resp.update) {
      info = resp.update;
      if (!info.checked) error = info.error || "не удалось проверить";
    } else {
      error = (resp && resp.error) || "хелпер не ответил";
    }
  }

  const latest = info && info.checked ? info.latest || "" : "";
  const helperLatest = (info && info.checked && info.helperLatest) || latest;

  state.updates = {
    checkedAt: Date.now(),
    error,
    pageUrl: (info && info.pageUrl) || "",
    addon: {
      current: addonCurrent,
      latest,
      // без данных проверки ничего не помечаем устаревшим
      outdated: !!latest && isNewer(latest, addonCurrent),
      unknown: !latest,
    },
    helper: {
      current: helperCurrent,
      latest: helperLatest,
      outdated: !!helperLatest && !!helperCurrent && isNewer(helperLatest, helperCurrent),
      unknown: !helperLatest || !helperCurrent,
      missing: !!state.helperMissing,
    },
  };
  notifyUI();
  return state.updates;
}

// Обновление хелпера в один клик: скачивание, сверка sha256, подмена бинаря и
// перезапуск демона делает сам хелпер. Здесь только запуск и понятный статус.
async function updateHelper() {
  if (state.updatingHelper) {
    return Promise.resolve({ ok: false, error: "обновление уже идёт" });
  }
  state.updatingHelper = true;
  notifyUI();
  try {
    // ждём долго: скачивание ~7 МБ плюс перезапуск демона
    const resp = await sendNative("self_update", {}, 180000);
    const res = (resp && resp.selfUpdate) || {};
    const ok = !!(resp && resp.ok);
    state.lastHelperUpdate = {
      at: Date.now(),
      ok,
      from: res.from || "",
      to: res.to || "",
      error: (resp && !resp.ok ? resp.error : "") || res.error || "",
    };
    if (ok) {
      state.helperVersion = res.to || state.helperVersion;
      state.updates = null; // версии изменились — проверим заново
      state.lastError = "";
    }
    // error на верхнем уровне: попап показывает именно его, а не «нет ответа»
    return { ok, error: state.lastHelperUpdate.error, result: state.lastHelperUpdate, state: publicState() };
  } catch (err) {
    const error = String((err && err.message) || err);
    state.lastHelperUpdate = { at: Date.now(), ok: false, error };
    return { ok: false, error, state: publicState() };
  } finally {
    state.updatingHelper = false;
    notifyUI();
    // демон перезапущен: поднимаем туннель и обновляем состояние
    try {
      await refresh();
    } catch (_) {}
    checkVersions(true).catch(() => {});
  }
}

/* -------------------------------------------------------------- operations */

async function doConnect() {
  // Не запускаем второе подключение поверх первого: хелпер перебирает порт × MTU,
  // это до 16 секунд, и раньше сторож успевал начать всё заново, обрывая перебор
  // до попытки на 443. Клиенту это выглядело как «подключается и ничего не происходит».
  if (state.connecting) return { skipped: "in-flight" };
  state.connecting = true;
  try {
    return await doConnectInner();
  } finally {
    state.connecting = false;
  }
}

async function doConnectInner() {
  // Первая попытка может попасть в момент, когда демон ещё поднимается (особенно на Windows
  // после перезагрузки). Один короткий повтор снимает жалобу «заводится только со второго раза».
  const first = await connectOnce();
  if (first && first.ok) {
    state.lastAttempt = { at: Date.now(), ok: true, error: "" };
    return first;
  }
  await new Promise((resolve) => setTimeout(resolve, 1500));
  const second = await connectOnce();
  const ok = !!(second && second.ok);
  state.lastAttempt = { at: Date.now(), ok, error: ok ? "" : (second && second.error) || "" };
  return second || first;
}

async function connectOnce() {
  const profile = activeProfile();
  if (!profile) return { ok: false, error: "нет сохранённого профиля — добавьте vpn:// ссылку" };
  const listen = state.settings.listenPort
    ? `127.0.0.1:${state.settings.listenPort}`
    : "127.0.0.1:0";
  const mtuAuto = !!state.settings.mtuAuto;
  const mtu = Number(state.settings.mtu) || 0;
  // при подборе MTU хелпер перебирает до четырёх значений по 8 с — ждём дольше
  const timeout = mtuAuto ? 60000 : 40000;
  // Порт 443 хелпер пробует сам: на сервере Amnezia может стоять «спутник»,
  // который слушает 443 и передаёт пакеты настоящему AWG. Клиенту ничего
  // вписывать не нужно — он просто пытается достучаться до 443.
  const resp = await sendNative("connect", { profile: profile.profile, listen, mtu, mtuAuto }, timeout);
  if (resp.ok && resp.status) {
    state.tunnel = { ...state.tunnel, ...normaliseStatus(resp.status) };
  } else {
    state.tunnel = { ...state.tunnel, running: false, state: "error", error: resp.error || "connect failed" };
  }
  updateBadge();
  notifyUI();
  return resp;
}

// Сторож: если туннель должен работать, а его нет — поднимаем. Пауза растёт, чтобы не
// заваливать сервер handshake'ами, когда он недоступен.
async function ensureTunnel() {
  const s = state.settings;
  if (!s.enabled || !s.autoConnect) return { skipped: "off" };
  if (!activeProfile()) return { skipped: "no-profile" };
  // «connecting» — это тоже «работает»: хелпер прямо сейчас перебирает порт и MTU.
  // Если считать это за простой, сторож запускает новое подключение и обрывает перебор.
  if (state.tunnel.running || state.tunnel.state === "connecting" || state.connecting) {
    state.retryDelay = 15000;
    state.nextRetryAt = 0;
    return { skipped: "running" };
  }
  if (Date.now() < state.nextRetryAt) return { skipped: "backoff" };

  const res = await doConnect();
  const alive = state.tunnel.running || state.tunnel.state === "connecting";
  if (alive) {
    state.retryDelay = 15000;
    state.nextRetryAt = 0;
  } else {
    state.nextRetryAt = Date.now() + state.retryDelay;
    state.retryDelay = Math.min(state.retryDelay * 2, 300000);
  }
  return res;
}

function normaliseStatus(st) {
  return {
    running: !!st.running,
    state: st.state || "stopped",
    socksPort: st.socksPort || state.tunnel.socksPort || 0,
    error: st.error || "",
    profileName: st.profileName || state.tunnel.profileName || "",
    endpoint: st.endpoint || state.tunnel.endpoint || "",
    lastHandshakeUnix: st.lastHandshakeUnix || 0,
    rxBytes: st.rxBytes || 0,
    txBytes: st.txBytes || 0,
    uptimeSec: st.uptimeSec || 0,
    mtu: st.mtu || 0,
    mtuAuto: !!st.mtuAuto,
    relay: !!st.relay,
  };
}

async function doDisconnect() {
  const resp = await sendNative("disconnect", {}, 15000);
  state.tunnel = { ...state.tunnel, running: false, state: "stopped", socksPort: 0, error: "" };
  updateBadge();
  notifyUI();
  return resp;
}

async function setEnabled(value) {
  await saveSettings({ enabled: !!value });
  if (value) {
    // явное включение сбрасывает отсрочку сторожа: пользователь ждёт подключения сейчас
    state.nextRetryAt = 0;
    state.retryDelay = 15000;
    const profile = activeProfile();
    if (profile) {
      const resp = await doConnect();
      return resp;
    }
  } else {
    await doDisconnect();
  }
  return { ok: true };
}

async function refreshStatus() {
  const resp = await sendNative("status", {}, 10000);
  if (!state.helperVersion) {
    const ver = await sendNative("version", {}, 8000);
    if (ver && ver.ok && ver.version) state.helperVersion = ver.version;
  }
  if (resp.ok && resp.status) {
    applyTunnelStatus(resp.status);
  } else if (!resp.ok) {
    state.tunnel = { ...state.tunnel, running: false, state: "stopped", socksPort: 0, error: resp.error || "" };
    updateBadge();
  }
  // Авто-лечение: включено, а туннеля нет (перезагрузка, падение демона, обновление бинаря).
  await ensureTunnel();
  // Проверку версий делаем один раз за сессию и не ждём её: она ходит в сеть.
  if (!state.updates && !state.helperMissing) checkVersions(false).catch(() => {});
  return publicState();
}

/* --------------------------------------------------------- profile handling */

async function addProfile(link) {
  const parsed = await awgbvDecodeVpn(link);
  const id = "p" + Date.now().toString(36) + Math.random().toString(36).slice(2, 6);
  const entry = {
    id,
    name: parsed.name || parsed.host,
    endpoint: `${parsed.host}:${parsed.port}`,
    container: parsed.container || "",
    clientIp: parsed.clientIp,
    addedAt: Date.now(),
    link: String(link).trim(),
    profile: parsed,
  };
  const profiles = [...state.settings.profiles, entry];
  await saveSettings({ profiles, activeProfileId: state.settings.activeProfileId || id });
  return { ok: true, id, profile: { id, name: entry.name, endpoint: entry.endpoint } };
}

async function removeProfile(id) {
  const profiles = state.settings.profiles.filter((p) => p.id !== id);
  let activeProfileId = state.settings.activeProfileId;
  if (activeProfileId === id) {
    activeProfileId = profiles.length ? profiles[0].id : null;
    if (state.tunnel.running) await doDisconnect();
  }
  await saveSettings({ profiles, activeProfileId });
  return { ok: true };
}

/* --------------------------------------------------------------- messaging */

browser.runtime.onMessage.addListener((msg) => {
  if (!msg || !msg.cmd) return undefined;
  switch (msg.cmd) {
    case "getState":
      return Promise.resolve({ ok: true, state: publicState() });
    case "updateHelper":
      return updateHelper();
    case "checkVersions":
      return checkVersions(!!msg.force)
        .then((updates) => ({ ok: true, updates, state: publicState() }))
        .catch((err) => ({ ok: false, error: String((err && err.message) || err) }));
    case "addProfile":
      return addProfile(msg.link).catch((err) => ({ ok: false, error: String((err && err.message) || err) }));
    case "removeProfile":
      return removeProfile(msg.id);
    case "selectProfile":
      return saveSettings({ activeProfileId: msg.id }).then(() => ({ ok: true }));
    case "setEnabled":
      return setEnabled(msg.value);
    case "connect":
      return doConnect();
    case "disconnect":
      return doDisconnect();
    case "recheck": {
      // мастер установки: пробуем соединение с нуля, без кэша порта
      state.nativePort = null;
      state.helperMissing = false;
      state.lastError = "";
      return refreshStatus().then(() => ({ ok: true, state: publicState() }));
    }
    case "refresh":
      return refreshStatus().then((s) => ({ ok: true, state: s }));
    case "saveLists":
      return saveSettings({
        whitelist: Array.isArray(msg.whitelist) ? msg.whitelist : state.settings.whitelist,
        blacklist: Array.isArray(msg.blacklist) ? msg.blacklist : state.settings.blacklist,
      }).then(() => ({ ok: true }));
    case "quitHelper": {
      return sendNative("quit", {}, 10000).then((resp) => {
        state.tunnel = { ...state.tunnel, running: false, state: "stopped", socksPort: 0 };
        state.helperVersion = "";
        updateBadge();
        notifyUI();
        return resp;
      });
    }
    case "savePrefs":
      return saveSettings({
        autoConnect: msg.autoConnect != null ? !!msg.autoConnect : state.settings.autoConnect,
        mtuAuto: msg.mtuAuto != null ? !!msg.mtuAuto : state.settings.mtuAuto,
        mtu: msg.mtu != null ? Number(msg.mtu) || 0 : state.settings.mtu,
        listenPort: msg.listenPort != null ? Number(msg.listenPort) || 0 : state.settings.listenPort,
      }).then(() => ({ ok: true, state: publicState() }));
    case "tabOverride": {
      const tabId = msg.tabId;
      const value = msg.value || "auto";
      if (value === "auto") state.tabOverride.delete(tabId);
      else state.tabOverride.set(tabId, value);
      notifyUI();
      return Promise.resolve({ ok: true });
    }
    case "listTabOverrides":
      return Promise.resolve({ ok: true, overrides: Array.from(state.tabOverride.entries()) });
    default:
      return Promise.resolve({ ok: false, error: "unknown cmd " + msg.cmd });
  }
});

browser.tabs.onRemoved.addListener((tabId) => {
  state.tabOverride.delete(tabId);
});

/* ------------------------------------------------------------- lifecycle */

browser.runtime.onStartup.addListener(async () => {
  await loadSettings();
  updateBadge();
  if (state.settings.enabled && state.settings.autoConnect) {
    await refreshStatus();
  }
});

browser.runtime.onInstalled.addListener(async () => {
  await loadSettings();
  await browser.alarms.create(POLL_ALARM, { periodInMinutes: 1 });
  updateBadge();
});

browser.alarms.onAlarm.addListener(async (alarm) => {
  if (alarm.name !== POLL_ALARM) return;
  if (!state.settings.enabled) return;
  await refreshStatus();
});

// Event page may be re-spawned at any time: settings must be loaded before the
// first proxy decision, so keep a promise the listener can rely on.
const ready = loadSettings().then(async () => {
  try {
    const info = await browser.runtime.getPlatformInfo();
    state.platformOs = info && info.os ? info.os : "";
  } catch (err) {
    state.platformOs = "";
  }
  await browser.alarms.create(POLL_ALARM, { periodInMinutes: 1 });
  updateBadge();
  if (state.settings.enabled && state.settings.autoConnect) {
    // Non-blocking: the tunnel is a daemon, this just re-syncs our view.
    refreshStatus();
  }
});
