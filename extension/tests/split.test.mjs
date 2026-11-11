/* Node-based tests for the extension logic that decides what goes into the
 * tunnel. background.js is a plain (non-module) script, so it can be evaluated
 * in a vm context with stubbed WebExtension APIs and then exercised directly.
 *
 * Note: top-level `const` in a script lands in the context's global *lexical*
 * scope, not on the sandbox object — so everything is read and written through
 * vm.runInContext() instead of ctx.someName.
 *
 * Run: node --test extension/tests/split.test.mjs
 */
import assert from "node:assert/strict";
import { test } from "node:test";
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";
import zlib from "node:zlib";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const extDir = path.resolve(here, "..");

function fakeKey(b) {
  return Buffer.alloc(32, b).toString("base64");
}

function buildVpnLink(doc) {
  const plain = Buffer.from(JSON.stringify(doc), "utf8");
  const packed = zlib.deflateSync(plain);
  const hdr = Buffer.alloc(4);
  hdr.writeUInt32BE(plain.length, 0);
  return "vpn://" + Buffer.concat([hdr, packed]).toString("base64url");
}

function fixtureLink(overrides = {}) {
  const last = {
    H1: "111111111-222222222",
    Jc: "6",
    S3: "46",
    S4: "16",
    I1: "<r 2><b 0x0102>",
    client_priv_key: fakeKey(1),
    server_pub_key: fakeKey(2),
    psk_key: fakeKey(3),
    client_ip: "10.99.7.14",
    hostName: "203.0.113.10",
    port: 40000,
    mtu: "1376",
    persistent_keep_alive: "25",
    ...overrides,
  };
  return buildVpnLink({
    containers: [
      {
        container: "amnezia-awg2",
        awg: { Jc: "6", port: "40000", last_config: JSON.stringify(last) },
      },
    ],
    description: "test-profile",
    dns1: "1.1.1.1",
    dns2: "1.0.0.1",
    hostName: "203.0.113.10",
  });
}

function stubBrowser(storage = {}) {
  const listeners = [];
  const noop = () => {};
  const promise = () => Promise.resolve();
  return {
    __listeners: listeners,
    proxy: {
      onRequest: { addListener: (fn) => listeners.push(fn) },
      onError: { addListener: noop },
    },
    storage: { local: { get: async () => storage, set: async () => {} } },
    action: { setBadgeText: promise, setBadgeBackgroundColor: promise, setTitle: promise },
    alarms: { create: promise, onAlarm: { addListener: noop } },
    tabs: { onRemoved: { addListener: noop }, query: async () => [] },
    runtime: {
      lastError: null,
      onMessage: { addListener: noop },
      onStartup: { addListener: noop },
      onInstalled: { addListener: noop },
      sendMessage: async () => {},
      connectNative: () => {
        throw new Error("no native host in tests");
      },
    },
  };
}

function loadBackground(storage = {}) {
  const ctx = vm.createContext({
    browser: stubBrowser(storage),
    console,
    setTimeout,
    clearTimeout,
    setInterval,
    clearInterval,
    atob,
    btoa,
    Blob,
    Response,
    TextDecoder,
    URL,
    DecompressionStream,
    Map,
    Promise,
  });
  vm.runInContext(fs.readFileSync(path.join(extDir, "lib/vpnparse.js"), "utf8"), ctx, { filename: "vpnparse.js" });
  vm.runInContext(fs.readFileSync(path.join(extDir, "background.js"), "utf8"), ctx, { filename: "background.js" });
  return ctx;
}

const ev = (ctx, expr) => vm.runInContext(expr, ctx);

// Prepare a deterministic runtime view: enabled, connected, given list mode.
function configure(ctx, { mode = "blacklist", whitelist = [], blacklist = [], enabled = true, running = true, port = 1080 } = {}) {
  ev(
    ctx,
    `state.settings = { ...state.settings, enabled: ${enabled}, mode: ${JSON.stringify(mode)},
       whitelist: ${JSON.stringify(whitelist)}, blacklist: ${JSON.stringify(blacklist)} };
     state.tunnel = { running: ${running}, state: ${JSON.stringify(running ? "connected" : "stopped")}, socksPort: ${running ? port : 0} };`
  );
}

test("vpn:// decoder maps an AmneziaWG share link", async () => {
  const ctx = loadBackground();
  const p = await ev(ctx, `awgbvDecodeVpn(${JSON.stringify(fixtureLink())})`);
  assert.equal(p.host, "203.0.113.10");
  assert.equal(p.port, 40000);
  assert.equal(p.clientIp, "10.99.7.14");
  assert.equal(p.mtu, 1376);
  assert.equal(p.keepalive, 25);
  assert.equal(p.container, "amnezia-awg2");
  assert.deepEqual(Array.from(p.dns), ["1.1.1.1", "1.0.0.1"]);
  assert.equal(p.awg.s3, "46");
  assert.equal(p.awg.i1, "<r 2><b 0x0102>");
  assert.equal(p.privateKey, fakeKey(1));
});

test("vpn:// decoder rejects a link without AmneziaWG", async () => {
  const ctx = loadBackground();
  const link = buildVpnLink({ containers: [{ container: "amnezia-openvpn", openvpn: {} }] });
  await assert.rejects(() => ev(ctx, `awgbvDecodeVpn(${JSON.stringify(link)})`), /AmneziaWG/);
});

test("domain matching handles subdomains and wildcards", () => {
  const ctx = loadBackground();
  const m = (host, list) => ev(ctx, `matchesAny(${JSON.stringify(host)}, ${JSON.stringify(list)})`);
  assert.equal(m("example.com", ["example.com"]), true);
  assert.equal(m("www.example.com", ["example.com"]), true);
  assert.equal(m("notexample.com", ["example.com"]), false);
  assert.equal(m("a.b.example.org", ["*.example.org"]), true);
  assert.equal(m("EXAMPLE.COM.", ["example.com"]), true);
  assert.equal(m("example.com", ["", "  ", "# comment"]), false);
  assert.equal(m("sub.example.net", [".example.net"]), true);
});

test("blacklist mode tunnels everything except listed hosts", () => {
  const ctx = loadBackground();
  configure(ctx, { mode: "blacklist", blacklist: ["intranet.corp", "*.local"] });
  assert.equal(ev(ctx, 'useTunnelFor("example.com", 1)'), true);
  assert.equal(ev(ctx, 'useTunnelFor("intranet.corp", 1)'), false);
  assert.equal(ev(ctx, 'useTunnelFor("printer.local", 1)'), false);
});

test("whitelist mode tunnels only listed hosts", () => {
  const ctx = loadBackground();
  configure(ctx, { mode: "whitelist", whitelist: ["whatismyip.example"] });
  assert.equal(ev(ctx, 'useTunnelFor("whatismyip.example", 1)'), true);
  assert.equal(ev(ctx, 'useTunnelFor("ru.wikipedia.org", 1)'), false);
});

test("loopback is never tunnelled", () => {
  const ctx = loadBackground();
  configure(ctx, { mode: "whitelist", whitelist: ["localhost", "local", "127.0.0.1"] });
  for (const host of ["localhost", "127.0.0.1", "::1", "router.local"]) {
    assert.equal(ev(ctx, `useTunnelFor(${JSON.stringify(host)}, 1)`), false, host);
  }
});

test("nothing is tunnelled while disabled or disconnected", () => {
  const ctx = loadBackground();
  configure(ctx, { mode: "blacklist", blacklist: [], enabled: false });
  assert.equal(ev(ctx, 'useTunnelFor("example.com", 1)'), false);
  configure(ctx, { mode: "blacklist", blacklist: [], enabled: true, running: false });
  assert.equal(ev(ctx, 'useTunnelFor("example.com", 1)'), false);
});

test("per-tab override wins over the lists", () => {
  const ctx = loadBackground();
  configure(ctx, { mode: "blacklist", blacklist: [] });
  ev(ctx, 'state.tabOverride.set(7, "direct")');
  assert.equal(ev(ctx, 'useTunnelFor("example.com", 7)'), false);
  assert.equal(ev(ctx, 'useTunnelFor("example.com", 8)'), true);
  configure(ctx, { mode: "whitelist", whitelist: [] });
  ev(ctx, 'state.tabOverride.set(8, "tunnel")');
  assert.equal(ev(ctx, 'useTunnelFor("example.com", 8)'), true);
});

test("proxy.onRequest returns SOCKS5 only for tunnelled hosts", () => {
  const ctx = loadBackground();
  configure(ctx, { mode: "whitelist", whitelist: ["tunnel.example"], port: 45678 });
  const viaProxy = ev(
    ctx,
    'browser.__listeners[0]({ url: "https://tunnel.example/p", host: "tunnel.example", tabId: 3 })'
  );
  assert.equal(viaProxy.type, "socks");
  assert.equal(viaProxy.host, "127.0.0.1");
  assert.equal(viaProxy.port, 45678);
  assert.equal(viaProxy.proxyDNS, true);
  assert.equal(viaProxy.version, 5);

  const direct = ev(
    ctx,
    'browser.__listeners[0]({ url: "https://direct.example/p", host: "direct.example", tabId: 3 })'
  );
  assert.equal(direct.type, "direct");
});

test("default settings keep traffic direct until the user opts in", async () => {
  const ctx = loadBackground();
  await ev(ctx, "loadSettings()");
  assert.equal(ev(ctx, "state.settings.enabled"), false);
  assert.equal(ev(ctx, 'useTunnelFor("example.com", 1)'), false);
});
