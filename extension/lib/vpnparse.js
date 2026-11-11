/* Amnezia `vpn://` share-link decoder.
 *
 * Wire format:  base64url( uint32be(len(unpacked)) || zlib(json) )
 * The JSON describes the server's containers; the concrete client config sits
 * in containers[].awg.last_config as a nested JSON string.
 *
 * Exposed as a global (no ESM) because Firefox event pages load background
 * scripts with a shared global scope.
 */

const AWGBV_VPN_PREFIX = "vpn://";

function awgbvBase64UrlToBytes(input) {
  let s = String(input).trim().replace(/^vpn:\/\//, "").replace(/\s+/g, "");
  s = s.replace(/-/g, "+").replace(/_/g, "/");
  while (s.length % 4 !== 0) s += "=";
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

async function awgbvInflate(bytes) {
  // "deflate" == zlib wrapper (RFC 1950), which is what Amnezia uses.
  const stream = new Blob([bytes]).stream().pipeThrough(new DecompressionStream("deflate"));
  const buf = await new Response(stream).arrayBuffer();
  return new Uint8Array(buf);
}

async function awgbvDecodeVpn(json) {
  const link = typeof json === "string" ? json : json && json.link;
  const raw = awgbvBase64UrlToBytes(link);
  const candidates = [];
  if (raw.length > 4) candidates.push(raw.subarray(4));
  candidates.push(raw);
  let plain = null;
  let lastErr = null;
  for (const body of candidates) {
    try {
      plain = await awgbvInflate(body);
      break;
    } catch (err) {
      lastErr = err;
    }
  }
  if (!plain) throw new Error("не удалось распаковать vpn:// (не zlib)");
  const doc = JSON.parse(new TextDecoder().decode(plain));
  const containers = doc.containers || [];
  const awgEntry = containers.find((c) => c && c.awg);
  if (!awgEntry) {
    throw new Error("в ссылке нет контейнера AmneziaWG — поддерживается только AmneziaWG (awg/awg2)");
  }
  const awg = awgEntry.awg;
  let last = null;
  if (typeof awg.last_config === "string" && awg.last_config) {
    try {
      last = JSON.parse(awg.last_config);
    } catch (err) {
      last = null;
    }
  }
  const pick = (key) => {
    if (last && typeof last[key] === "string" && last[key]) return last[key];
    if (typeof awg[key] === "string") return awg[key];
    return "";
  };
  const portStr = pick("port") || String(awg.port || "");
  const profile = {
    name: doc.description || (pick("hostName") || "AmneziaWG"),
    host: pick("hostName") || doc.hostName || "",
    port: parseInt(portStr, 10) || 0,
    privateKey: pick("client_priv_key"),
    peerPublicKey: pick("server_pub_key"),
    presharedKey: pick("psk_key"),
    clientIp: pick("client_ip"),
    mtu: parseInt(pick("mtu"), 10) || 1420,
    keepalive: parseInt(pick("persistent_keep_alive"), 10) || 25,
    dns: [doc.dns1, doc.dns2].filter(Boolean),
    container: awgEntry.container || "",
    awg: {
      jc: pick("Jc"),
      jmin: pick("Jmin"),
      jmax: pick("Jmax"),
      s1: pick("S1"),
      s2: pick("S2"),
      s3: pick("S3"),
      s4: pick("S4"),
      h1: pick("H1"),
      h2: pick("H2"),
      h3: pick("H3"),
      h4: pick("H4"),
      i1: pick("I1"),
      i2: pick("I2"),
      i3: pick("I3"),
      i4: pick("I4"),
      i5: pick("I5"),
    },
  };
  if (!profile.host || !profile.port || !profile.privateKey || !profile.peerPublicKey || !profile.clientIp) {
    throw new Error("в ссылке не хватает полей (host/port/keys/ip)");
  }
  if (!profile.dns.length) profile.dns = ["1.1.1.1", "1.0.0.1"];
  return profile;
}

globalThis.awgbvDecodeVpn = awgbvDecodeVpn;
