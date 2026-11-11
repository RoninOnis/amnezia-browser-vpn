/* Ссылка vpn://, собранная панелью администратора (Go), должна разбираться нашим
 * же расширением. Фикстура генерируется тестом `TestWritePanelLinkFixture` в
 * `helper/peerops_test.go`, поэтому этот файл ловит расхождение форматов между
 * Go и JS — «панель выдала ключ, а аддон его не понимает» иначе всплывёт только
 * у пользователя.
 *
 * Run: node --test extension/tests/panel-link.test.mjs
 */
import assert from "node:assert/strict";
import { test } from "node:test";
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const extDir = path.resolve(here, "..");
const fixturePath = path.join(here, "fixtures", "panel-link.txt");

function loadParser() {
  const ctx = vm.createContext({
    console, atob, btoa, Blob, Response, TextDecoder, URL, DecompressionStream, Promise,
  });
  vm.runInContext(fs.readFileSync(path.join(extDir, "lib/vpnparse.js"), "utf8"), ctx, {
    filename: "vpnparse.js",
  });
  return ctx;
}

test("ссылка от панели разбирается расширением", async () => {
  const link = fs.readFileSync(fixturePath, "utf8").trim();
  assert.ok(link.startsWith("vpn://"), "фикстура должна быть ссылкой vpn://");

  const ctx = loadParser();
  const profile = await vm.runInContext(`awgbvDecodeVpn(${JSON.stringify(link)})`, ctx);

  assert.equal(profile.host, "vpn.example.com");
  assert.equal(profile.port, 36853);
  assert.equal(profile.clientIp, "10.8.1.9", "внутренний адрес пира должен дойти");
  assert.equal(profile.privateKey, "ERERERERERERERERERERERERERERERERERERERERERE=");
  assert.equal(profile.peerPublicKey, "REREREREREREREREREREREREREREREREREREREREREQ=");
  assert.equal(profile.presharedKey, "MzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzM=");
  assert.equal(profile.name, "панель");
  // параметры обфускации обязаны пережить упаковку: без них handshake не пройдёт
  assert.equal(profile.awg.jc, "5");
  assert.equal(profile.awg.jmin, "50");
  assert.equal(profile.awg.s1, "40");
  assert.equal(profile.awg.h1, "1234567");
  assert.deepEqual(Array.from(profile.dns), ["1.1.1.1", "1.0.0.1"]);
});

test("ссылка от панели не содержит лишних полей вместо нужных", async () => {
  const link = fs.readFileSync(fixturePath, "utf8").trim();
  const ctx = loadParser();
  const profile = await vm.runInContext(`awgbvDecodeVpn(${JSON.stringify(link)})`, ctx);
  // ключи должны быть валидным base64 на 32 байта, иначе клиент не запустится
  for (const key of ["privateKey", "peerPublicKey", "presharedKey"]) {
    const raw = Buffer.from(profile[key], "base64");
    assert.equal(raw.length, 32, `${key} должен быть 32 байта, а не ${raw.length}`);
  }
  assert.ok(profile.mtu >= 1280, "MTU должен быть задан");
  assert.ok(profile.keepalive > 0, "keepalive должен быть задан");
});
