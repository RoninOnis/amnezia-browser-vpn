import assert from "node:assert/strict";
import { createRequire } from "node:module";
import test from "node:test";

const require = createRequire(import.meta.url);
const { installGuide, RELEASE_BASE } = require("../lib/install-guide.js");

test("Windows ставится одним .cmd, без PowerShell", () => {
  const g = installGuide("win");
  assert.equal(g.supported, true);
  assert.deepEqual(g.files, ["install-windows.cmd"]);
  assert.ok(!g.files.some((f) => f.endsWith(".ps1")), "ps1 не должен предлагаться");
  assert.match(g.command, /install-windows\.cmd$/);
  assert.ok(g.steps.length >= 4, "должно быть не меньше четырёх шагов");
  assert.equal(g.downloadUrls[0], `${RELEASE_BASE}/install-windows.cmd`);
});

test("Linux тянет shell-установщик и даёт команду без sudo", () => {
  const g = installGuide("linux");
  assert.deepEqual(g.files, ["install-linux.sh"]);
  assert.equal(g.command, "bash ~/Downloads/install-linux.sh");
  assert.ok(!/sudo/.test(g.command), "sudo в команде недопустим");
  assert.equal(g.downloadUrls[0], `${RELEASE_BASE}/install-linux.sh`);
});

test("macOS честно помечен как неподдерживаемый", () => {
  const g = installGuide("mac");
  assert.equal(g.supported, false);
  assert.deepEqual(g.files, []);
  assert.deepEqual(g.steps, []);
  assert.ok(g.intro.length > 0);
});

test("неизвестная платформа не ломает мастер", () => {
  for (const os of ["", undefined, "android", "foo"]) {
    const g = installGuide(os);
    assert.equal(g.supported, false);
    assert.deepEqual(g.downloadUrls, []);
    assert.ok(g.issuesUrl.startsWith("https://github.com/"));
  }
});

test("шаги описывают скачивание, запуск и возврат в браузер", () => {
  const titles = installGuide("win").steps.map((s) => s.title.toLowerCase()).join(" | ");
  assert.match(titles, /скачайте/);
  assert.match(titles, /запустите/);
  assert.match(titles, /вернитесь/);
});
