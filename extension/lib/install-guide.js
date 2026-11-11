/* Мастер установки помощника: тексты шагов под каждую систему.
 *
 * Чистые данные и одна функция — чтобы это можно было протестировать в node
 * (extension/tests/install-guide.test.mjs) и не держать формулировки в popup.js.
 */

const RELEASE_BASE = "https://github.com/RoninOnis/amnezia-browser-vpn/releases/latest/download";

const GUIDES = {
  win: {
    os: "Windows",
    supported: true,
    files: ["install-windows.cmd"],
    intro: "Помощник — небольшая отдельная программа: браузер не умеет поднимать туннель сам. "
      + "Минута, права администратора не нужны.",
    steps: [
      {
        title: "Скачайте установщик",
        body: "Кнопка ниже скачает один файл — install-windows.cmd.",
        download: true,
      },
      {
        title: "Откройте папку «Загрузки»",
        body: "Проводник → Загрузки. Если браузер спросит, что делать с файлом, выберите «Сохранить».",
      },
      {
        title: "Запустите файл",
        body: "Двойной клик по install-windows.cmd → «Выполнить». Скрипт сам скачает помощника, "
          + "сверит контрольную сумму и подключит его к Firefox.",
      },
      {
        title: "Вернитесь в браузер",
        body: "Нажмите «Проверить снова». Если не нашёлся — перезапустите Firefox.",
      },
    ],
    command: 'cd /d "%USERPROFILE%\\Downloads" && install-windows.cmd',
    fallback: {
      label: "Если окно закрылось мгновенно или Windows блокирует запуск, выполните в командной строке:",
    },
  },

  linux: {
    os: "Linux",
    supported: true,
    files: ["install-linux.sh"],
    intro: "Помощник — небольшая отдельная программа: браузер не умеет поднимать туннель сам. "
      + "Ставится в домашний каталог, root и sudo не нужны.",
    steps: [
      {
        title: "Скачайте установщик",
        body: "Кнопка ниже скачает install-linux.sh — один файл, ничего собирать не нужно.",
        download: true,
      },
      {
        title: "Откройте терминал",
        body: "Подойдёт любой: Ctrl+Alt+T в большинстве систем, либо терминал из меню приложений.",
      },
      {
        title: "Выполните команду",
        body: "Вставьте скопированную строку. Скрипт определит архитектуру, скачает нужный бинарь, "
          + "сверит sha256 и пропишет манифест для вашего профиля.",
        command: "bash ~/Downloads/install-linux.sh",
        copy: true,
      },
      {
        title: "Вернитесь в браузер",
        body: "Нажмите «Проверить снова». Если не нашёлся — перезапустите Firefox.",
      },
    ],
    command: "bash ~/Downloads/install-linux.sh",
    fallback: {
      label: "Если браузер — snap, установщик сам положит манифест внутрь песочницы. "
        + "Запускать через sudo не нужно и вредно: манифест уедет в /root.",
    },
  },

  mac: {
    os: "macOS",
    supported: false,
    files: [],
    intro: "Для macOS готового бинаря пока нет — есть сборки под Linux и Windows.",
    steps: [],
    fallback: {
      label: "Если нужен macOS — напишите в issues репозитория, добавим сборку.",
    },
  },
};

function installGuide(platformOs) {
  const key = String(platformOs || "").toLowerCase();
  const guide = GUIDES[key] || GUIDES.mac;
  return {
    ...guide,
    platform: key || "unknown",
    downloadUrls: guide.files.map((name) => `${RELEASE_BASE}/${name}`),
    issuesUrl: "https://github.com/RoninOnis/amnezia-browser-vpn/issues",
  };
}

// для node-тестов и на случай, если popup загрузит скрипт как модуль
if (typeof module !== "undefined" && module.exports) {
  module.exports = { installGuide, GUIDES, RELEASE_BASE };
}
