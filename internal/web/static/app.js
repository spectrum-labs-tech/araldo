// Araldo dashboard behavior. Everything works without it; this only adds
// conveniences (ADR 0015).
(function () {
  "use strict";

  // Selects that submit their form on change.
  document.querySelectorAll("[data-autosubmit]").forEach(function (el) {
    el.addEventListener("change", function () { el.form.requestSubmit(); });
  });

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

  // Radio groups that show [data-when="name=value"] blocks.
  function syncToggles() {
    document.querySelectorAll("[data-when]").forEach(function (block) {
      var parts = block.getAttribute("data-when").split("=");
      var checked = document.querySelector('input[name="' + parts[0] + '"]:checked');
      block.hidden = !checked || checked.value !== parts[1];
    });
  }
  document.querySelectorAll("[data-toggle]").forEach(function (el) { el.addEventListener("change", syncToggles); });
  syncToggles();

  // Copy buttons.
  document.querySelectorAll("[data-copy]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var el = document.getElementById(btn.getAttribute("data-copy"));
      if (!el || !navigator.clipboard) { return; }
      navigator.clipboard.writeText(el.textContent.trim()).then(function () {
        var old = btn.textContent;
        btn.textContent = "Copied";
        setTimeout(function () { btn.textContent = old; }, 1500);
      });
    });
  });

  // Live template preview.
  var form = document.getElementById("template-form");
  var out = document.getElementById("preview");
  if (form && out) {
    var timer = null;
    var render = function () {
      var data = new FormData(form);
      data.set("csrf", out.getAttribute("data-csrf"));
      fetch("/preview", { method: "POST", body: data, credentials: "same-origin" })
        .then(function (r) { return r.json(); })
        .then(function (res) { show(res); })
        .catch(function () { out.textContent = "Preview unavailable."; });
    };
    var show = function (res) {
      out.replaceChildren();
      if (res.error) {
        var p = document.createElement("div");
        p.className = "alert";
        p.textContent = res.error;
        (res.problems || []).forEach(function (pr) {
          var li = document.createElement("div");
          li.className = "small";
          li.textContent = (pr.param ? pr.param + ": " : "") + pr.message;
          p.appendChild(li);
        });
        out.appendChild(p);
        return;
      }
      (res.renditions || []).forEach(function (r) {
        var box = document.createElement("div");
        box.className = "rendition" + (r.violations.length ? " bad" : "");
        var head = document.createElement("div");
        head.className = "r-head";
        var name = document.createElement("strong");
        name.textContent = r.provider;
        var lim = document.createElement("span");
        lim.className = "muted small";
        lim.textContent = r.lengths.join(" + ") + " / " + r.limit + " " + r.counting;
        head.append(name, lim);
        box.appendChild(head);
        r.parts.forEach(function (part) {
          var pre = document.createElement("pre");
          pre.className = "post-text";
          pre.textContent = part;
          box.appendChild(pre);
        });
        r.violations.forEach(function (v) {
          var warn = document.createElement("p");
          warn.className = "alert small";
          warn.textContent = v.message;
          box.appendChild(warn);
        });
        out.appendChild(box);
      });
    };
    form.querySelectorAll("[data-preview]").forEach(function (el) {
      el.addEventListener("input", function () { clearTimeout(timer); timer = setTimeout(render, 350); });
      el.addEventListener("change", function () { clearTimeout(timer); timer = setTimeout(render, 50); });
    });
    render();
  }
})();
