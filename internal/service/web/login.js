"use strict";

// Proof of work for the "email me a code" forms. Before the form is sent the
// browser finds a nonce so that SHA-256(challenge + ":" + nonce) starts with
// data-pow zero bits (about a second). The server checks it in microseconds
// and only then emails a code. Without JavaScript the server refuses to send.
(() => {
  const K = new Uint32Array([
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
  ]);
  const W = new Uint32Array(64);
  const H = new Uint32Array(8);

  // SHA-256 of buf[0..len) (ASCII, short); buf is reused and must have room
  // for the padding. Leaves the digest in H.
  const sha256 = (buf, len) => {
    const blocks = (len + 9 + 63) >> 6;
    buf.fill(0, len, blocks * 64);
    buf[len] = 0x80;
    const bitLen = len * 8;
    buf[blocks * 64 - 2] = bitLen >>> 8;
    buf[blocks * 64 - 1] = bitLen & 0xff;
    H[0] = 0x6a09e667; H[1] = 0xbb67ae85; H[2] = 0x3c6ef372; H[3] = 0xa54ff53a;
    H[4] = 0x510e527f; H[5] = 0x9b05688c; H[6] = 0x1f83d9ab; H[7] = 0x5be0cd19;
    for (let block = 0; block < blocks; block++) {
      const o = block * 64;
      for (let t = 0; t < 16; t++) {
        const j = o + t * 4;
        W[t] = (buf[j] << 24) | (buf[j + 1] << 16) | (buf[j + 2] << 8) | buf[j + 3];
      }
      for (let t = 16; t < 64; t++) {
        const x = W[t - 15], y = W[t - 2];
        const s0 = ((x >>> 7) | (x << 25)) ^ ((x >>> 18) | (x << 14)) ^ (x >>> 3);
        const s1 = ((y >>> 17) | (y << 15)) ^ ((y >>> 19) | (y << 13)) ^ (y >>> 10);
        W[t] = W[t - 16] + s0 + W[t - 7] + s1;
      }
      let a = H[0], b = H[1], c = H[2], d = H[3], e = H[4], f = H[5], g = H[6], h = H[7];
      for (let t = 0; t < 64; t++) {
        const S1 = ((e >>> 6) | (e << 26)) ^ ((e >>> 11) | (e << 21)) ^ ((e >>> 25) | (e << 7));
        const t1 = (h + S1 + ((e & f) ^ (~e & g)) + K[t] + W[t]) | 0;
        const S0 = ((a >>> 2) | (a << 30)) ^ ((a >>> 13) | (a << 19)) ^ ((a >>> 22) | (a << 10));
        const t2 = (S0 + ((a & b) ^ (a & c) ^ (b & c))) | 0;
        h = g; g = f; f = e; e = (d + t1) | 0; d = c; c = b; b = a; a = (t1 + t2) | 0;
      }
      H[0] += a; H[1] += b; H[2] += c; H[3] += d; H[4] += e; H[5] += f; H[6] += g; H[7] += h;
    }
  };

  const zeroBits = () => {
    for (let i = 0; i < 8; i++) {
      if (H[i] !== 0) return i * 32 + Math.clz32(H[i]);
    }
    return 256;
  };

  // Find the nonce in slices so the page stays responsive.
  const solve = (challenge, bits, done) => {
    const prefix = challenge + ":";
    const buf = new Uint8Array(256);
    for (let i = 0; i < prefix.length; i++) buf[i] = prefix.charCodeAt(i) & 0x7f;
    let nonce = 0;
    const slice = () => {
      const end = nonce + 25000;
      for (; nonce < end; nonce++) {
        const digits = String(nonce);
        let len = prefix.length;
        for (let i = 0; i < digits.length; i++) buf[len++] = digits.charCodeAt(i);
        sha256(buf, len);
        if (zeroBits() >= bits) {
          done(digits);
          return;
        }
      }
      setTimeout(slice, 0);
    };
    setTimeout(slice, 0);
  };

  for (const form of document.querySelectorAll("form[data-pow]")) {
    const bits = Number(form.dataset.pow);
    const status = form.querySelector("[data-pow-status]");
    const button = form.querySelector("button[type=submit]");
    let busy = false;
    form.addEventListener("submit", (event) => {
      if (form.elements.nonce.value !== "") return; // solved: let it go
      event.preventDefault();
      if (busy) return;
      busy = true;
      button.disabled = true;
      if (status) status.textContent = "Checking your browser… this takes a second or two.";
      solve(form.elements.pow.value, bits, (nonce) => {
        form.elements.nonce.value = nonce;
        if (status) status.textContent = "Sending…";
        form.submit();
      });
    });
  }

  // A challenge works once. Coming back to this page from history would
  // reuse it, so fetch a fresh page instead.
  window.addEventListener("pageshow", (event) => {
    if (event.persisted) location.reload();
  });
})();
