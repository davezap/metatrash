"use strict";

// Passkeys. The server renders WebAuthn options as JSON (binary values in
// base64url) in data attributes; this script asks the browser for a passkey,
// puts the answer into the form's hidden fields (base64url) and submits the
// form normally. The server verifies everything; nothing here is trusted.
//
//   form[data-passkey-get]    sign in, or confirm on Your account
//                             (data-conditional: also offer passkeys in the
//                             email field's autofill)
//   form[data-passkey-create] add a passkey on Your account
(() => {
  const supported = !!(window.PublicKeyCredential && navigator.credentials);

  const decode = (text) => {
    const b64 = text.replace(/-/g, "+").replace(/_/g, "/");
    const raw = atob(b64 + "===".slice((b64.length + 3) % 4));
    const out = new Uint8Array(raw.length);
    for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
    return out.buffer;
  };
  const encode = (buffer) => {
    const bytes = new Uint8Array(buffer);
    let raw = "";
    for (let i = 0; i < bytes.length; i++) raw += String.fromCharCode(bytes[i]);
    return btoa(raw).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  };
  const status = (form, text) => {
    const el = form.querySelector("[data-passkey-status]");
    if (el) el.textContent = text;
  };
  const explain = (err) => {
    switch (err && err.name) {
      case "NotAllowedError":
        return "Cancelled or timed out. Try again when you're ready.";
      case "InvalidStateError":
        return "This device or password manager already holds a passkey for your account.";
      case "SecurityError":
        return "Passkeys are not available on this address.";
      default:
        return "Your browser could not use a passkey" + (err && err.message ? ": " + err.message : ".");
    }
  };

  const requestOptions = (json) => {
    const options = JSON.parse(json);
    options.challenge = decode(options.challenge);
    if (options.allowCredentials) {
      options.allowCredentials = options.allowCredentials.map((c) => ({ ...c, id: decode(c.id) }));
    }
    return options;
  };
  const fillAssertion = (form, credential) => {
    const r = credential.response;
    form.elements.credential.value = encode(credential.rawId);
    form.elements.client_data.value = encode(r.clientDataJSON);
    form.elements.authenticator_data.value = encode(r.authenticatorData);
    form.elements.signature.value = encode(r.signature);
    form.elements.user_handle.value = r.userHandle ? encode(r.userHandle) : "";
  };

  for (const form of document.querySelectorAll("form[data-passkey-get]")) {
    if (!supported) continue; // stays hidden
    form.hidden = false;
    let busy = false;
    let autofill = null;
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      if (busy) return;
      busy = true;
      if (autofill) autofill.abort();
      status(form, "Waiting for your passkey…");
      try {
        const credential = await navigator.credentials.get({ publicKey: requestOptions(form.dataset.passkeyGet) });
        fillAssertion(form, credential);
        status(form, "Signing in…");
        form.submit();
      } catch (err) {
        status(form, explain(err));
        busy = false;
      }
    });
    // Conditional UI: passkeys appear in the email field's suggestions.
    if (form.hasAttribute("data-conditional") && PublicKeyCredential.isConditionalMediationAvailable) {
      PublicKeyCredential.isConditionalMediationAvailable().then((available) => {
        if (!available || busy) return;
        autofill = new AbortController();
        return navigator.credentials
          .get({ mediation: "conditional", publicKey: requestOptions(form.dataset.passkeyGet), signal: autofill.signal })
          .then((credential) => {
            if (!credential || busy) return;
            busy = true;
            fillAssertion(form, credential);
            status(form, "Signing in…");
            form.submit();
          });
      }).catch(() => {});
    }
  }

  for (const form of document.querySelectorAll("form[data-passkey-create]")) {
    let busy = false;
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      if (!supported) {
        status(form, "This browser does not support passkeys.");
        return;
      }
      if (busy) return;
      busy = true;
      status(form, "Follow your browser's prompts…");
      try {
        const options = JSON.parse(form.dataset.passkeyCreate);
        options.challenge = decode(options.challenge);
        options.user.id = decode(options.user.id);
        options.excludeCredentials = (options.excludeCredentials || []).map((c) => ({ ...c, id: decode(c.id) }));
        const credential = await navigator.credentials.create({ publicKey: options });
        const r = credential.response;
        form.elements.client_data.value = encode(r.clientDataJSON);
        form.elements.attestation.value = encode(r.attestationObject);
        form.elements.transports.value = typeof r.getTransports === "function" ? r.getTransports().join(",") : "";
        status(form, "Saving…");
        form.submit();
      } catch (err) {
        status(form, explain(err));
        busy = false;
      }
    });
  }
})();
