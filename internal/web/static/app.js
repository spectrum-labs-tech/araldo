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

})();
