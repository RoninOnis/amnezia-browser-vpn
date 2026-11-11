/* Тест-драйвер панели: выполняется ВНУТРИ живого попапа расширения.
 *
 * Зачем так: Firefox WebDriver отказывается работать в привилегированном
 * контексте moz-extension:// («does not support browsing contexts in privileged
 * scope»), поэтому внешний запуск нажатий невозможен. Драйвер подключается к
 * самой странице попапа (тот же глобальный лексический скоуп, что и у
 * popup.js), жмёт настоящие кнопки и отправляет результат на локальный сервер.
 *
 * Так проверяется именно UI: селекторы, обработчики, гонка с опросом каждые 4 с,
 * тексты. Заглушка DOM в node-тестах этого не ловит.
 */
(function () {
  const PORT = __PORT__;
  const steps = [];
  const log = (ok, label, extra) => {
    steps.push({ ok: !!ok, label, extra: extra === undefined ? "" : String(extra) });
  };
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const waitFor = async (fn, ms) => {
    const t0 = Date.now();
    while (Date.now() - t0 < (ms || 30000)) {
      try {
        const v = fn();
        if (v) return v;
      } catch (err) {
        /* состояние ещё не готово */
      }
      await sleep(500);
    }
    return null;
  };
  const report = async (payload) => {
    payload.steps = steps;
    payload.done = true;
    try {
      await fetch(`http://127.0.0.1:${PORT}/result`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
    } catch (err) {
      document.title = "REPORT-FAILED " + err;
    }
  };

  (async () => {
    const payload = { error: "", peersBefore: null, peersAfterCreate: null, peersAfterRevoke: null,
                      link: "", linkShown: "", profilesBefore: 0, profilesAfterAdopt: 0,
                      profilesAfterSecondAdopt: 0, remotePeers: null };
    try {
      // 1. состояние и панель
      const ready = await waitFor(() => (typeof state !== "undefined" && state ? state : null), 60000);
      log(!!ready, "состояние попапа загрузилось");
      if (!ready) throw new Error("state не появился");

      showScreen("admin");
      const screenVisible = await waitFor(
        () => document.getElementById("screen-admin").classList.contains("active"), 10000);
      log(!!screenVisible, "вкладка «Панель» открывается");

      const servers = await waitFor(() => (state.servers || []).length > 0, 60000);
      log(!!servers, "сервер из хелпера доехал в попап", (state.servers || []).map((s) => s.host).join(","));

      const peers = await waitFor(() => (state.peers ? state.peers : null), 60000);
      log(!!peers, "список ключей загрузился", peers ? (peers.peers || []).length + " пиров" : "нет");
      payload.peersBefore = peers ? (peers.peers || []).length : -1;
      payload.prevIps = peers ? (peers.peers || []).map((p) => p.allowedIps) : [];
      payload.remotePeers = peers ? (peers.container || "") + " udp/" + (peers.listenPort || "") : "";
      log(!!(peers && peers.listenPort), "в ответе есть порт контейнера", payload.remotePeers);

      const connectHidden = document.getElementById("adminConnectCard").hidden;
      log(!!connectHidden, "форма SSH скрыта, сервер уже настроен");

      // 2. выпуск нового ключа
      window.prompt = () => "panel-e2e";
      window.confirm = () => true;
      document.getElementById("btnAdminCreate").click();
      const key = await waitFor(
        () => (state.lastPeerKey && state.lastPeerKey.vpnLink ? state.lastPeerKey : null), 90000);
      log(!!key, "ключ выпущен", key ? key.allowedIps : "нет");
      payload.link = key ? key.vpnLink : "";
      payload.keyPublicKey = key ? (key.publicKey || "") : "";
      payload.peersAfterCreate = (await waitFor(
        () => (state.peers && state.peers.peers.length > payload.peersBefore ? state.peers.peers.length : null),
        60000)) || -1;
      log(payload.peersAfterCreate === payload.peersBefore + 1, "пир появился в списке",
          payload.peersBefore + " → " + payload.peersAfterCreate);

      const shown = await waitFor(() => {
        const v = document.getElementById("admKeyLink").value;
        return v && v.startsWith("vpn://") ? v : null;
      }, 15000);
      payload.linkShown = shown || "";
      log(!!shown, "ссылка видна в карточке ключа", shown ? shown.length + " символов" : "пусто");
      log(!document.getElementById("adminKeyCard").hidden, "карточка ключа раскрыта");

      // 3. добавление в свои серверы + защита от дубликата
      payload.profilesBefore = (state.profiles || []).length;
      const adopt = document.getElementById("btnAdminAdopt");
      adopt.click();
      await waitFor(() => (state.profiles || []).length > payload.profilesBefore, 30000);
      payload.profilesAfterAdopt = (state.profiles || []).length;
      log(payload.profilesAfterAdopt === payload.profilesBefore + 1, "профиль добавлен в «Серверы»",
          payload.profilesBefore + " → " + payload.profilesAfterAdopt);
      // после успеха кнопка должна исчезнуть/заблокироваться: иначе легко наплодить дубликатов
      const hiddenAfter = document.getElementById("btnAdminAdopt").hidden;
      log(!!hiddenAfter, "кнопка добавления скрыта после успеха");
      try {
        document.getElementById("btnAdminAdopt").click();
      } catch (err) {
        /* скрытую кнопку жать нечего */
      }
      await sleep(3000);
      payload.profilesAfterSecondAdopt = (state.profiles || []).length;
      log(payload.profilesAfterSecondAdopt === payload.profilesAfterAdopt,
          "повторное добавление не создало дубликат",
          payload.profilesAfterAdopt + " → " + payload.profilesAfterSecondAdopt);

      // 4. отзыв проверяем на ВТОРОМ ключе: первый нужен внешнему тесту, чтобы
      // проверить подключение (отозвав его сразу, мы бы ломали собственный сценарий).
      window.prompt = () => "panel-e2e-revoke";
      const count1 = (state.peers.peers || []).length;
      document.getElementById("btnAdminCreate").click();
      const keyB = await waitFor(() => {
        const k = state.lastPeerKey;
        return k && k.allowedIps !== key.allowedIps ? k : null;
      }, 90000);
      log(!!keyB, "второй ключ выпущен для проверки отзыва", keyB ? keyB.allowedIps : "нет");
      await waitFor(() => (state.peers.peers || []).length === count1 + 1, 60000);
      // ВАЖНО: жать первую кнопку «Отозвать» нельзя — список отсортирован по IP,
      // и так можно снести чужого клиента (ровно это и случилось один раз).
      const wantIp = (keyB && keyB.allowedIps) || "";
      const revoked = (() => {
        const rows = Array.prototype.slice.call(document.querySelectorAll("#admPeers .list-item"));
        const row = rows.find((r) => wantIp && r.textContent.indexOf(wantIp) >= 0);
        if (!row) return false;
        const btn = Array.prototype.slice.call(row.querySelectorAll("button"))
          .find((b) => b.textContent.indexOf("Отозвать") >= 0);
        if (!btn) return false;
        btn.click();
        return true;
      })();
      log(revoked, "нашлась строка выпущенного ключа " + wantIp);
      payload.peersAfterRevoke = (await waitFor(
        () => (state.peers && state.peers.peers.length === count1 ? state.peers.peers.length : null),
        60000)) || -1;
      log(payload.peersAfterRevoke === count1, "ключ отозван и исчез из списка",
          (count1 + 1) + " → " + payload.peersAfterRevoke);
      const stillThere = (state.peers.peers || []).some((p) => p.allowedIps === wantIp);
      log(!stillThere, "из списка ушёл именно отозванный ключ (" + wantIp + ")");
      const keyAlive = (state.peers.peers || []).some((p) => p.allowedIps === key.allowedIps);
      log(keyAlive, "ключ для подключения остался на месте (" + key.allowedIps + ")");

      // чужие ключи не тронуты: сверяем, что все прежние адреса на месте
      const nowIps = (state.peers.peers || []).map((p) => p.allowedIps);
      payload.othersIntact = payload.prevIps.every((ip) => nowIps.indexOf(ip) >= 0);
      log(payload.othersIntact, "остальные клиенты не задеты",
          payload.prevIps.length + " адресов проверено");

      // 5. ни одна ошибка не показана красным без причины
      const hints = ["admConnectHint", "admPeerHint", "admKeyHint"]
        .map((id) => (document.getElementById(id) || {}).textContent || "")
        .filter((t) => t.indexOf("Не удалось") >= 0);
      log(hints.length === 0, "ошибок в подсказках нет", hints.join(" | "));
    } catch (err) {
      payload.error = String(err && err.stack ? err.stack : err);
      log(false, "исключение в драйвере", payload.error);
    }
    await report(payload);
  })();
})();
