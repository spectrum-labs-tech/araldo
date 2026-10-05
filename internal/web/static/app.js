// Araldo dashboard behavior. Everything works without it; this only adds
// conveniences (ADR 0015).
(function () {
  "use strict";

  // The org/mode switch returns to the current page.
  document.querySelectorAll('form.ctx input[name="back"]').forEach(function (el) {
    el.value = location.pathname;
  });

  // Confirmations for destructive actions.
  document.querySelectorAll("form[data-confirm]").forEach(function (form) {
    form.addEventListener("submit", function (e) {
      if (!confirm(form.getAttribute("data-confirm"))) { e.preventDefault(); }
    });
  });

  // Radio groups and selects that show [data-when="name=value"] blocks.
  function currentValue(name) {
    var select = document.querySelector('select[name="' + name + '"]');
    if (select) { return select.value; }
    var checked = document.querySelector('input[name="' + name + '"]:checked');
    return checked ? checked.value : null;
  }
  function syncToggles() {
    document.querySelectorAll("[data-when]").forEach(function (block) {
      var parts = block.getAttribute("data-when").split("=");
      block.hidden = currentValue(parts[0]) !== parts[1];
    });
  }
  document.querySelectorAll("[data-toggle]").forEach(function (el) { el.addEventListener("change", syncToggles); });
  syncToggles();

  // Copy buttons. The result is announced to screen readers too.
  document.querySelectorAll("[data-copy]").forEach(function (btn) {
    var status = document.createElement("span");
    status.className = "sr-only";
    status.setAttribute("role", "status");
    btn.after(status);
    btn.addEventListener("click", function () {
      var el = document.getElementById(btn.getAttribute("data-copy"));
      if (!el || !navigator.clipboard) { return; }
      navigator.clipboard.writeText(el.textContent.trim()).then(function () {
        var old = btn.textContent;
        btn.textContent = "Copied";
        status.textContent = "Copied to the clipboard.";
        setTimeout(function () { btn.textContent = old; status.textContent = ""; }, 1500);
      });
    });
  });

  // Passkeys (ADR 0007). Each [data-passkey] button runs a WebAuthn
  // ceremony: options from the server, the browser's prompt, the answer
  // back. Without WebAuthn the buttons stay hidden.
  var ceremonies = {
    login: { options: "/login/passkey/options", finish: "/login/passkey", get: true },
    mfa: { options: "/login/mfa/passkey/options", finish: "/login/mfa/passkey", get: true },
    confirm: { options: "/confirm/passkey/options", finish: "/confirm/passkey", get: true },
    register: { options: "/account/passkeys/options", finish: "/account/passkeys", get: false }
  };
  function fromB64(s) {
    var bin = atob(s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4));
    var out = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) { out[i] = bin.charCodeAt(i); }
    return out.buffer;
  }
  function toB64(buf) {
    var bytes = new Uint8Array(buf), bin = "";
    for (var i = 0; i < bytes.length; i++) { bin += String.fromCharCode(bytes[i]); }
    return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }
  function decodeOptions(pk) {
    pk.challenge = fromB64(pk.challenge);
    if (pk.user) { pk.user.id = fromB64(pk.user.id); }
    (pk.excludeCredentials || []).concat(pk.allowCredentials || []).forEach(function (c) { c.id = fromB64(c.id); });
    return pk;
  }
  function encodeCredential(cred) {
    var r = cred.response, out = { id: cred.id, rawId: toB64(cred.rawId), type: cred.type,
      authenticatorAttachment: cred.authenticatorAttachment || undefined,
      clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {}, response: {} };
    out.response.clientDataJSON = toB64(r.clientDataJSON);
    if (r.attestationObject) {
      out.response.attestationObject = toB64(r.attestationObject);
      if (r.getTransports) { out.response.transports = r.getTransports(); }
    } else {
      out.response.authenticatorData = toB64(r.authenticatorData);
      out.response.signature = toB64(r.signature);
      if (r.userHandle) { out.response.userHandle = toB64(r.userHandle); }
    }
    return out;
  }
  function post(url, csrf, body) {
    var headers = { "Content-Type": "application/json" };
    if (csrf) { headers["X-CSRF-Token"] = csrf; }
    return fetch(url, { method: "POST", headers: headers, body: JSON.stringify(body), credentials: "same-origin" })
      .then(function (res) { return res.json().then(function (data) { if (!res.ok) { throw data; } return data; }); });
  }
  if (window.PublicKeyCredential && navigator.credentials) {
    document.querySelectorAll("[data-passkey-area]").forEach(function (el) { el.hidden = false; });
    document.querySelectorAll("[data-passkey]").forEach(function (btn) {
      var c = ceremonies[btn.getAttribute("data-passkey")];
      var area = btn.closest("[data-passkey-area]");
      var errorEl = area && area.querySelector("[data-passkey-error]");
      var csrf = btn.getAttribute("data-csrf");
      btn.addEventListener("click", function () {
        if (errorEl) { errorEl.textContent = ""; }
        btn.disabled = true;
        var token;
        post(c.options, csrf, {}).then(function (data) {
          token = data.token;
          var pk = decodeOptions(data.options.publicKey);
          return c.get ? navigator.credentials.get({ publicKey: pk }) : navigator.credentials.create({ publicKey: pk });
        }).then(function (cred) {
          var nameEl = btn.getAttribute("data-name") && document.querySelector(btn.getAttribute("data-name"));
          return post(c.finish, csrf, { token: token, response: encodeCredential(cred), next: btn.getAttribute("data-next") || "",
            name: nameEl ? nameEl.value : "" });
        }).then(function (data) {
          location.assign(data.next || "/");
        }).catch(function (err) {
          btn.disabled = false;
          if (err && err.confirm) { location.assign(err.confirm); return; }
          if (errorEl) {
            errorEl.textContent = (err && err.error) ||
              (err && err.name === "NotAllowedError" ? "The passkey prompt was closed or timed out." : "That did not work. Try again.");
          }
        });
      });
    });
  }

})();
