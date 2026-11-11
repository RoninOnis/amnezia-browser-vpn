/* Слепой прогон: первый запуск, инструкции нет, человек видит аддон впервые.
 *
 * Это не проверка «правильно ли работает», а проверка «понятно ли, что делать».
 * Проба ничего не настраивает: состояние пустое (ни профиля, ни сервера, ни
 * списков), и она честно проходит пять вкладок, собирая то, что видит человек:
 * все видимые тексты, все нажимаемые элементы и что происходит после нажатия.
 *
 * Ищет ровно те тупики, из-за которых новичок «как слепой котёнок»:
 *   - есть ли на каждом экране ответ «что делать дальше»;
 *   - не обещает ли главная кнопка действие, которого не будет без профиля;
 *   - понимает ли человек слова «пир», «спутник», «хелпер», «handshake»;
 *   - молчит ли кнопка или объясняет отказ;
 *   - нет ли экрана, где нужен пароль root без объяснения, зачем.
 */
(function () {
  const PORT = __PORT__;
  const steps = [];
  const findings = [];
  const log = (ok, label, extra) => {
    steps.push({ ok: !!ok, label, extra: extra === undefined ? "" : String(extra) });
  };
  const note = (label, text) => {
    findings.push({ label, text: String(text) });
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

  const report = async (payload) => {
    payload.steps = steps;
    payload.findings = findings;
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

  // весь видимый текст экрана: так его прочтёт человек, без знания разметки
  const screenText = (id) => {
    const sec = document.getElementById(id);
    if (!sec) return "";
    const walk = (node, out) => {
      if (!node || node.hidden) return out;
      const cls = (node.className && node.className.baseVal !== undefined)
        ? node.className.baseVal : (node.className || "");
      if (typeof cls === "string" && /(^|\s)hidden(\s|$)/.test(cls)) return out;
      if (node.nodeType === 3) {
        const t = (node.textContent || "").trim();
        if (t) out.push(t);
        return out;
      }
      if (node.nodeType === 1 && /^(SVG|PATH|RECT|CIRCLE)$/i.test(node.tagName)) return out;
      for (const child of node.childNodes || []) walk(child, out);
      return out;
    };
    return walk(sec, []).join(" | ").replace(/\s+/g, " ");
  };

  // что нажимается: кнопки, переключатели, поля
  const controls = (id) => {
    const sec = document.getElementById(id);
    if (!sec) return [];
    return [...sec.querySelectorAll("button, input, select, .site-switch button")]
      .filter((el) => {
        const cls = el.className || "";
        if (typeof cls === "string" && /(^|\s)hidden(\s|$)/.test(cls)) return false;
        if (el.closest(".hidden")) return false;
        return true;
      })
      .map((el) => {
        const label = (el.textContent || "").trim() || el.value || el.id;
        return label.length > 46 ? label.slice(0, 46) + "…" : label;
      });
  };

  const JARGON = ["пир", "пиры", "спутник", "хелпер", "handshake", "allowedIps", "egress",
              "PSK", "контейнер", "релей", "MTU", "SOCKS"];
  const has = (text, word) => new RegExp(`(^|[^а-яёa-z])${word}([^а-яёa-z]|$)`, "i")
    .test(text);

  (async () => {
    const payload = { error: "" };
    try {
      await wait(() => (typeof state !== "undefined" && state ? state : null), 60000);
      // ничего не настраиваем: это первый запуск
      note("состояние на входе", JSON.stringify({
        профилей: (state.profiles || []).length,
        серверов: (state.servers || []).length,
        сайтов: (state.whitelist || []).length,
        хелпер: state.helperVersion || "(нет)",
        helperMissing: !!state.helperMissing,
      }));

      // ---------------------------------------------- 1. главная
      showScreen("home");
      await sleep(1500);
      const homeText = screenText("screen-home");
      note("ЭКРАН «Главная»", homeText);
      note("кнопки на «Главной»", controls("screen-home").join(" / "));

      // главная кнопка обещает «нажмите, чтобы подключиться» — а профиля нет
      const heroSub = (document.getElementById("heroSub") || {}).textContent || "";
      const heroLabel = (document.getElementById("heroLabel") || {}).textContent || "";
      note("главная кнопка", `${heroLabel} · ${heroSub}`);
      log(!/нажмите, чтобы подключиться/i.test(heroSub) || (state.profiles || []).length > 0,
          "главная кнопка не обещает подключение, когда подключаться некуда",
          `${heroLabel} · ${heroSub}`);

      // круг без подписи не читается как кнопка: на первом запуске нужна явная
      const firstKey = document.getElementById("btnFirstKey");
      log(firstKey && !firstKey.hidden, "есть явная кнопка «Добавить ключ vpn://»",
          firstKey ? (firstKey.hidden ? "спрятана" : "видна") : "нет элемента");
      firstKey.click();
      await sleep(800);
      const afterFirst = document.querySelector(".screen.active");
      log(afterFirst && afterFirst.id === "screen-servers",
          "она ведёт на «Серверы»", afterFirst ? afterFirst.id : "?");
      const focused = document.activeElement && document.activeElement.id;
      log(focused === "newLink", "и ставит курсор в поле ключа", focused || "нет фокуса");
      showScreen("home");
      await sleep(300);

      // «переподключиться» без сервера — ложная надежда
      const recon = document.getElementById("btnReconnect");
      log(!recon || recon.classList.contains("hidden"),
          "«переподключиться» не показывается, когда нечего переподключать",
          recon ? recon.className : "нет элемента");

      // что будет, если новичок нажмёт большую кнопку сразу
      document.getElementById("heroBtn").click();
      await sleep(1500);
      const screenNow = document.querySelector(".screen.active");
      const alertText = (document.getElementById("alertText") || {}).textContent || "";
      const lastError = state.lastError || "";
      const addHint = (document.getElementById("addHint") || {}).textContent || "";
      note("после нажатия главной кнопки",
           `экран: ${screenNow ? screenNow.id : "?"} | подсказка: ${addHint || "(пусто)"}`
           + ` | alert: ${alertText || "(пусто)"}`);
      log(screenNow && screenNow.id === "screen-servers",
          "большая кнопка без профиля ведёт туда, где это исправляется",
          screenNow ? screenNow.id : "?");
      log(/vpn:/.test(addHint || alertText),
          "и там сразу сказано, что вставлять", addHint || alertText);
      log(!/bad json|undefined|ReferenceError|TypeError/i.test(addHint + alertText),
          "в объяснении нет технического мусора", addHint || alertText);
      showScreen("home");
      await sleep(400);

      // ---------------------------------------------- 2. серверы
      showScreen("servers");
      await sleep(1200);
      const srvText = screenText("screen-servers");
      note("ЭКРАН «Серверы»", srvText);
      note("кнопки на «Серверах»", controls("screen-servers").join(" / "));
      log(/vpn:\/\//.test(srvText), "на пустом экране «Серверы» сказано про ключ vpn://", srvText.slice(0, 160));
      log(/amnezia|амнезия/i.test(srvText), "сказано, откуда взять ключ (приложение Amnezia)",
          /amnezia|амнезия/i.test(srvText) ? "да" : "ни слова");
      log(/С чего начать/i.test(srvText), "на пустых «Серверах» есть блок «С чего начать»",
          /С чего начать/i.test(srvText) ? "да" : "нет");
      log(/Добавить профиль/i.test(srvText) && /Главную/i.test(srvText),
          "шаги называют, что нажать и куда вернуться", srvText.slice(0, 200));

      // ---------------------------------------------- 3. сайты
      showScreen("sites");
      await sleep(1200);
      const sitesText = screenText("screen-sites");
      note("ЭКРАН «Сайты»", sitesText);
      note("кнопки на «Сайтах»", controls("screen-sites").join(" / "));

      // ---------------------------------------------- 4. панель
      showScreen("admin");
      await sleep(1200);
      const adminText = screenText("screen-admin");
      note("ЭКРАН «Панель»", adminText);
      const adminHasPassword = !!document.getElementById("admPassword");
      log(!adminHasPassword || /необязательно|нужен доступ|root|сервер амнезии|для администратора/i
            .test(adminText),
          "на «Панели» объяснено, что это доступ к серверу и он не обязателен",
          adminText.slice(0, 200));
      note("кнопки на «Панели»", controls("screen-admin").join(" / "));
      log(/необязательно/i.test(adminText),
          "на «Панели» прямо сказано, что раздел необязательный", adminText.slice(0, 120));
      const noServer = document.getElementById("btnNoServer");
      log(!!noServer, "есть выход «у меня нет своего сервера»", noServer ? "есть" : "нет");
      if (noServer) {
        noServer.click();
        await sleep(700);
        const now = document.querySelector(".screen.active");
        log(now && now.id === "screen-servers", "этот выход ведёт к ключу", now ? now.id : "?");
      }

      // ---------------------------------------------- 5. настройки
      showScreen("settings");
      await sleep(1200);
      const setText = screenText("screen-settings");
      note("ЭКРАН «Настройки»", setText);
      note("кнопки на «Настройках»", controls("screen-settings").join(" / "));

      // ---------------------------------------------- жаргон
      const all = [homeText, srvText, sitesText, adminText, setText].join(" ");
      const used = JARGON.filter((w) => has(all, w));
      note("жаргон в интерфейсе", used.join(", ") || "нет");
      log(used.filter((w) => !["MTU", "SOCKS"].includes(w)).length === 0,
          "в интерфейсе нет слов, которых новичок не поймёт",
          used.join(", ") || "чисто");

      // «хелпер» — главное непонятное слово: он есть в тексте установки
      log(!has(all, "хелпер"), "слово «хелпер» не используется в интерфейсе",
          has(all, "хелпер") ? "встречается" : "нет");

      payload.ok = steps.every((s) => s.ok);
    } catch (err) {
      payload.error = String(err && (err.stack || err.message) || err);
    }
    await report(payload);
  })();
})();
