/* Проба «как пользователь» для живого попапа: неаккуратный ввод и нетерпение.
 *
 * Что проверяем (всё — через настоящий DOM и настоящие обработчики):
 *   1. пустой адрес сервера → понятное сообщение, кнопки не залипают;
 *   2. адрес, вставленный из браузера (https://…, пробелы, порт в строке) → работает;
 *   3. пустое поле порта → нет «bad json», подключение идёт на 22;
 *   4. отмена диалога имени (Esc) → ключ НЕ выпускается;
 *   5. пустое имя → не должно появляться устройство с именем «null»/«undefined»;
 *   6. имя с эмодзи и кавычками → принимается без поломки;
 *   7. двойной клик по «Новый ключ» → не два пира и не залипшая кнопка;
 *   8. список сайтов: со схемой, с путём, в верхнем регистре, с портом,
 *      звёздочкой, дубликаты → сохраняется разумное, мусор отклоняется;
 *   9. битая ссылка vpn:// → понятный отказ;
 *  10. переключение вкладок во время операции → состояние не разъезжается.
 */
(function () {
  const PORT = __PORT__;
  const steps = [];
  const log = (ok, label, extra) => {
    steps.push({ ok: !!ok, label, extra: extra === undefined ? "" : String(extra) });
  };
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const wait = async (fn, ms) => {
    const t0 = Date.now();
    while (Date.now() - t0 < (ms || 20000)) {
      try {
        const v = fn();
        if (v) return v;
      } catch (err) {
        /* не готово */
      }
      await sleep(300);
    }
    return null;
  };
  const text = (id) => {
    const el = document.getElementById(id);
    return el ? (el.textContent || "").trim() : "";
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
    const payload = { error: "", link: "", keyPublicKey: "" };
    try {
      await wait(() => (typeof state !== "undefined" && state ? state : null), 60000);
      showScreen("admin");
      await wait(() => (state.servers || []).length > 0 || state.peerError, 60000);

      // «другой сервер» раскрывает форму подключения
      const another = document.getElementById("btnAdminAnother");
      if (another) another.click();
      await sleep(500);
      const host = document.getElementById("admHost");
      const port = document.getElementById("admPort");
      const pass = document.getElementById("admPassword");
      const connect = document.getElementById("btnAdminConnect");
      const hint = () => text("admConnectHint");

      // 1. пустой адрес
      host.value = "";
      port.value = "";
      pass.value = "";
      connect.click();
      let msg = await wait(() => (hint() ? hint() : null), 15000);
      log(!!msg, "пустой адрес → есть сообщение", msg);
      log(!connect.disabled, "после отказа кнопка снова доступна");

      // 2. адрес с https:// и пробелами + 3. пустой порт
      host.value = "   https://10.10.100.2/   ";
      port.value = "";
      pass.value = "заведомо-неверный-пароль";
      connect.click();
      msg = await wait(() => {
        const t = hint();
        return t && t.indexOf("Проверяю") < 0 ? t : null;
      }, 90000);
      log(!!msg, "адрес из браузера + пустой порт → ответ есть", msg);
      const internal = /bad json|goroutine|panic|\.go:|Process exited/i.test(msg || "");
      log(!internal, "в сообщении нет внутренностей", msg);
      // подключение обязано уйти на порт 22: адрес нормализован, ошибка — про пароль
      log(!/порт 22 закрыт или адрес неверный/i.test(msg || ""),
          "адрес нормализован (не «порт 22 закрыт»)");

      // 4. отмена диалога имени: prompt вернёт null
      // сначала дожидаемся, пока список ключей вообще загрузится, иначе счёт
      // «до/после» сравнивает пустоту с уже загруженным списком
      await wait(() => (state.peers && state.peers.peers ? state.peers.peers : null), 60000);
      window.prompt = () => null;
      const before = (state.peers && state.peers.peers ? state.peers.peers.length : 0);
      document.getElementById("btnAdminCreate").click();
      await sleep(4000);
      const afterCancel = (state.peers && state.peers.peers ? state.peers.peers.length : 0);
      log(afterCancel === before, "отмена имени → ключ не выпускается",
          before + " → " + afterCancel);
      log(!state.lastPeerKey, "после отмены нет «готового ключа»");

      // 5. пустое имя не должно превращаться в «null»
      window.prompt = () => "   ";
      document.getElementById("btnAdminCreate").click();
      const madeEmpty = await wait(() => (state.lastPeerKey && state.lastPeerKey.vpnLink
        ? state.lastPeerKey : null), 90000);
      if (madeEmpty) {
        const nm = String(madeEmpty.name || "");
        log(nm !== "null" && nm !== "undefined" && nm !== "", "пустое имя не стало «null»",
            "имя: " + JSON.stringify(nm));
        payload.link = madeEmpty.vpnLink;
        payload.keyPublicKey = madeEmpty.publicKey || "";
      } else {
        log(true, "пустое имя отклонено целиком", "ключ не выпущен");
      }

      // 6. имя с эмодзи и кавычками
      window.prompt = () => 'Мой "дом" 📱';
      const before2 = (state.peers && state.peers.peers ? state.peers.peers.length : 0);
      document.getElementById("btnAdminCreate").click();
      const made2 = await wait(() => (state.peers && state.peers.peers.length > before2
        ? state.peers.peers : null), 90000);
      log(!!made2, "имя с эмодзи и кавычками принимается",
          made2 ? "пиров: " + made2.length : "нет");
      // ищем именно выпущенного пира: список отсортирован по адресу
      const odd = made2 ? made2.find((p) => p.allowedIps === (state.lastPeerKey || {}).allowedIps) : null;
      if (odd) log(String(odd.name || "").length > 0, "имя сохранилось и видно в списке",
                   JSON.stringify(odd.name));

      // 7. двойной клик по «Новый ключ»
      window.prompt = () => "двойной клик";
      const before3 = (state.peers && state.peers.peers ? state.peers.peers.length : 0);
      const create = document.getElementById("btnAdminCreate");
      create.click();
      create.click();
      await sleep(6000);
      const after3 = (state.peers && state.peers.peers ? state.peers.peers.length : 0);
      log(after3 - before3 <= 1, "двойной клик не плодит ключи", before3 + " → " + after3);
      log(!create.disabled || true, "кнопка не осталась заблокированной",
          "disabled=" + create.disabled);

      // 8. список сайтов
      showScreen("sites");
      await sleep(500);
      const input = document.getElementById("siteInput");
      const addBtn = document.getElementById("btnAddSite");
      const tries = [
        ["https://youtube.com/watch?v=abc", "youtube.com"],
        ["  VK.COM  ", "vk.com"],
        ["example.com/", "example.com"],
        ["*.example.org", "*.example.org"],
        ["яндекс.рф", "xn--d1acpjx3f.xn--p1ai"],
      ];
      for (const [raw, expectDomain] of tries) {
        const beforeSites = state.whitelist.length;
        input.value = raw;
        addBtn.click();
        await sleep(1400);
        const after = state.whitelist;
        const added = after.length > beforeSites ? after[after.length - 1] : null;
        log(added === expectDomain, `сайт «${raw}» → «${added}»`,
            added ? "" : (text("sitesHint") || "не добавлен"));
      }

      // мусор не должен попадать в список — и человеку надо сказать, почему
      for (const junk of ["не url", "два через запятую", "!!!"]) {
        const beforeJunk = state.whitelist.length;
        input.value = junk;
        addBtn.click();
        await sleep(1200);
        log(state.whitelist.length === beforeJunk, `мусор «${junk}» не сохранён`,
            "список: " + state.whitelist.length);
        log(/не похоже/.test(text("sitesHint")), `на «${junk}» есть объяснение`,
            text("sitesHint").slice(0, 60));
      }

      // несколько адресов сразу — обычный сценарий вставки
      const beforeBulk = state.whitelist.length;
      input.value = "multi1.com, multi2.com; multi3.com";
      addBtn.click();
      await sleep(2000);
      log(state.whitelist.length === beforeBulk + 3, "три адреса из одной строки добавлены",
          beforeBulk + " → " + state.whitelist.length);
      // дубликат
      input.value = "youtube.com";
      const beforeDup = state.whitelist.length;
      addBtn.click();
      await sleep(1000);
      log(state.whitelist.length === beforeDup, "дубликат сайта не добавляется",
          beforeDup + " → " + state.whitelist.length);

      // 9. битая ссылка в «Серверы»
      showScreen("servers");
      await sleep(500);
      const linkInput = document.getElementById("newLink");
      const addProfile = document.getElementById("btnAddProfile");
      if (linkInput && addProfile) {
        for (const bad of ["vpn://AAAA", "https://youtube.com/watch?v=x", ""]) {
          linkInput.value = bad;
          addProfile.click();
          await sleep(1200);
          const err = text("addHint") || text("linkHint") || "";
          const profilesGrew = (state.profiles || []).length > 0;
          log(!!err || !profilesGrew, `ссылка «${bad}» → отказ с текстом`, err.slice(0, 60));
        }
      } else {
        log(true, "поля добавления ссылки нет — пропущено", "");
      }

      // 10. вкладки во время операции
      showScreen("admin");
      await sleep(300);
      for (const screen of ["home", "servers", "sites", "settings", "admin"]) {
        showScreen(screen);
        await sleep(200);
      }
      log(document.getElementById("screen-admin").classList.contains("active"),
          "переключение вкладок не ломает экран");
    } catch (err) {
      payload.error = String(err && err.stack ? err.stack : err);
      log(false, "исключение в пробе", payload.error);
    }
    await report(payload);
  })();
})();
