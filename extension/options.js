const $ = (sel) => document.querySelector(sel);

async function send(cmd, extra = {}) {
  try {
    return await browser.runtime.sendMessage({ cmd, ...extra });
  } catch (err) {
    return { ok: false, error: String((err && err.message) || err) };
  }
}

function linesToList(text) {
  return String(text || "")
    .split(/\r?\n/)
    .map((s) => s.trim())
    .filter(Boolean);
}

function renderProfiles(s) {
  const box = $("#profiles");
  box.innerHTML = "";
  if (!s.profiles.length) {
    box.innerHTML = '<div class="hint">Профилей пока нет. Вставьте vpn:// ссылку ниже.</div>';
    return;
  }
  for (const p of s.profiles) {
    const row = document.createElement("div");
    row.className = "profile-row";

    const radio = document.createElement("input");
    radio.type = "radio";
    radio.name = "activeProfile";
    radio.checked = p.id === s.activeProfileId;
    radio.addEventListener("change", async () => {
      await send("selectProfile", { id: p.id });
      await refresh();
    });

    const name = document.createElement("div");
    name.className = "name";
    name.innerHTML = `<div>${escapeHtml(p.name || p.endpoint)}</div>
      <div class="meta">${escapeHtml(p.endpoint)} · ${escapeHtml(p.clientIp || "")} ${p.container ? "· " + escapeHtml(p.container) : ""}</div>`;

    const del = document.createElement("button");
    del.className = "btn danger";
    del.textContent = "Удалить";
    del.addEventListener("click", async () => {
      if (!confirm("Удалить профиль «" + (p.name || p.endpoint) + "»?")) return;
      await send("removeProfile", { id: p.id });
      await refresh();
    });

    row.append(radio, name, del);
    box.appendChild(row);
  }
}

function escapeHtml(str) {
  return String(str == null ? "" : str).replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

function renderStatus(s) {
  const t = s.tunnel || {};
  const line = $("#statusLine");
  if (!t.running) {
    line.className = "status";
    line.textContent = t.error ? "остановлено · " + t.error : "остановлено";
    return;
  }
  line.className = "status " + (t.state === "connected" ? "ok" : t.state === "error" ? "err" : "warn");
  const bits = [t.state || "?"];
  if (t.socksPort) bits.push(`SOCKS5 127.0.0.1:${t.socksPort}`);
  if (t.endpoint) bits.push(t.endpoint);
  if (t.lastHandshakeUnix) bits.push("handshake " + new Date(t.lastHandshakeUnix * 1000).toLocaleString());
  if (t.uptimeSec) bits.push("аптайм " + t.uptimeSec + "с");
  bits.push(`↓${t.rxBytes || 0} ↑${t.txBytes || 0} байт`);
  line.textContent = bits.join(" · ");
}

let last = null;

async function refresh() {
  const res = await send("getState");
  if (!res || !res.state) return;
  const s = res.state;
  last = s;
  renderProfiles(s);
  renderStatus(s);
  document.querySelectorAll('input[name="mode"]').forEach((el) => {
    el.checked = el.value === s.mode;
  });
  $("#whitelist").value = (s.whitelist || []).join("\n");
  $("#blacklist").value = (s.blacklist || []).join("\n");
  $("#autoConnect").checked = !!s.autoConnect;
  $("#listenPort").value = s.listenPort || 0;
}

$("#addProfile").addEventListener("click", async () => {
  const link = $("#newLink").value.trim();
  if (!link) return;
  const hint = $("#addHint");
  hint.textContent = "разбираю…";
  const res = await send("addProfile", { link });
  if (res && res.ok) {
    hint.textContent = "добавлен: " + res.profile.name;
    $("#newLink").value = "";
    await refresh();
  } else {
    hint.textContent = "ошибка: " + ((res && res.error) || "?");
  }
});

$("#saveLists").addEventListener("click", async () => {
  const mode = (document.querySelector('input[name="mode"]:checked') || {}).value || "blacklist";
  const res = await send("saveLists", {
    mode,
    whitelist: linesToList($("#whitelist").value),
    blacklist: linesToList($("#blacklist").value),
  });
  $("#listsHint").textContent = res && res.ok ? "сохранено" : "ошибка: " + ((res && res.error) || "?");
  await refresh();
});

$("#savePrefs").addEventListener("click", async () => {
  const res = await send("savePrefs", {
    autoConnect: $("#autoConnect").checked,
    listenPort: Number($("#listenPort").value) || 0,
  });
  $("#prefsHint").textContent = res && res.ok ? "сохранено" : "ошибка: " + ((res && res.error) || "?");
  await refresh();
});

$("#btnConnect").addEventListener("click", async () => {
  await send("setEnabled", { value: true });
  await refresh();
});

$("#btnDisconnect").addEventListener("click", async () => {
  await send("disconnect");
  await refresh();
});

$("#btnRefresh").addEventListener("click", refresh);

browser.runtime.onMessage.addListener((msg) => {
  if (msg && msg.type === "state" && msg.payload) {
    renderProfiles(msg.payload);
    renderStatus(msg.payload);
  }
});

refresh();
setInterval(refresh, 5000);
