const $ = (sel) => document.querySelector(sel);

let currentState = null;
let currentTabId = null;
let currentHost = "";

function fmtBytes(n) {
  if (!n) return "0 B";
  const units = ["B", "КБ", "МБ", "ГБ"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n.toFixed(i ? 1 : 0)} ${units[i]}`;
}

function fmtUptime(sec) {
  if (!sec) return "—";
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = sec % 60;
  return h ? `${h}ч ${m}м` : m ? `${m}м ${s}с` : `${s}с`;
}

function stateLabel(t) {
  if (!t) return { text: "нет данных", cls: "" };
  if (t.state === "connected") return { text: "подключено", cls: "ok" };
  if (t.state === "connecting") return { text: "подключение…", cls: "warn" };
  if (t.state === "error") return { text: "ошибка: " + (t.error || "?"), cls: "err" };
  return { text: "отключено", cls: "" };
}

async function send(cmd, extra = {}) {
  try {
    return await browser.runtime.sendMessage({ cmd, ...extra });
  } catch (err) {
    return { ok: false, error: String((err && err.message) || err) };
  }
}

function render() {
  const s = currentState;
  if (!s) return;
  $("#toggle").checked = !!s.enabled;

  const t = s.tunnel || {};
  const label = stateLabel(t);
  const status = $("#status");
  status.className = "status " + label.cls;
  const parts = [label.text];
  if (t.running && t.socksPort) parts.push(`SOCKS5 127.0.0.1:${t.socksPort}`);
  if (t.running && t.uptimeSec) parts.push(`аптайм ${fmtUptime(t.uptimeSec)}`);
  if (t.running) parts.push(`↓${fmtBytes(t.rxBytes)} ↑${fmtBytes(t.txBytes)}`);
  status.textContent = parts.join(" · ");

  // profiles
  const sel = $("#profile");
  const prev = sel.value;
  sel.innerHTML = "";
  if (!s.profiles.length) {
    const opt = document.createElement("option");
    opt.value = "";
    opt.textContent = "— добавьте vpn:// ссылку в настройках —";
    sel.appendChild(opt);
    sel.disabled = true;
  } else {
    sel.disabled = false;
    for (const p of s.profiles) {
      const opt = document.createElement("option");
      opt.value = p.id;
      opt.textContent = p.name || p.endpoint;
      if (p.id === s.activeProfileId) opt.selected = true;
      sel.appendChild(opt);
    }
    if (prev && s.profiles.some((p) => p.id === prev)) sel.value = prev;
  }
  $("#profileInfo").textContent = s.activeProfile
    ? `${s.activeProfile.endpoint}${t.lastHandshakeUnix ? " · handshake " + new Date(t.lastHandshakeUnix * 1000).toLocaleTimeString() : ""}`
    : "профиль не выбран";

  // lists summary
  const mode = s.mode === "whitelist" ? "белый список (в VPN только перечисленное)" : "чёрный список (в VPN всё, кроме перечисленного)";
  $("#listInfo").textContent = `${mode} · белых: ${s.whitelist.length}, чёрных: ${s.blacklist.length}`;

  // current site
  $("#siteHost").textContent = currentHost || "—";

  // per-tab override
  send("listTabOverrides").then((res) => {
    const overrides = new Map(res && res.overrides ? res.overrides : []);
    const cur = overrides.get(currentTabId) || "auto";
    for (const btn of document.querySelectorAll("#tabSeg .seg-btn")) {
      btn.classList.toggle("active", btn.dataset.value === cur);
    }
  });

  const err = $("#error");
  if (s.lastError) {
    err.textContent = s.lastError;
    err.classList.remove("hidden");
  } else if (!s.profiles.length) {
    err.textContent = "Хелпер установлен? Добавьте профиль (vpn://) в настройках.";
    err.classList.remove("hidden");
  } else {
    err.classList.add("hidden");
  }
}

async function refresh() {
  const res = await send("refresh");
  if (res && res.state) {
    currentState = res.state;
    render();
  }
}

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
    currentState = res.state;
    render();
  }
  refresh();
}

$("#toggle").addEventListener("change", async (ev) => {
  const res = await send("setEnabled", { value: ev.target.checked });
  if (res && res.ok === false && res.error) {
    $("#error").textContent = res.error;
    $("#error").classList.remove("hidden");
  }
  await refresh();
});

$("#profile").addEventListener("change", async (ev) => {
  await send("selectProfile", { id: ev.target.value });
  await refresh();
});

for (const btn of document.querySelectorAll("#tabSeg .seg-btn")) {
  btn.addEventListener("click", async () => {
    await send("tabOverride", { tabId: currentTabId, value: btn.dataset.value });
    for (const b of document.querySelectorAll("#tabSeg .seg-btn")) b.classList.toggle("active", b === btn);
  });
}

$("#openOptions").addEventListener("click", () => browser.runtime.openOptionsPage());
$("#refresh").addEventListener("click", refresh);

browser.runtime.onMessage.addListener((msg) => {
  if (msg && msg.type === "state" && msg.payload) {
    currentState = msg.payload;
    render();
  }
});

init();
