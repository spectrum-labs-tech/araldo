// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The template editor (ADR 0015): code editors for the template bodies and
// JSON fields, per-platform tabs, and the live preview. Bundled with esbuild
// into internal/web/static/editor.js by `task web:js`; never edit the
// output. The page works without it: plain textareas, stacked sections, no
// live preview.
import { EditorState } from "@codemirror/state";
import { EditorView, keymap, lineNumbers, highlightActiveLine, drawSelection, placeholder } from "@codemirror/view";
import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
import { StreamLanguage, syntaxHighlighting, HighlightStyle, bracketMatching, indentOnInput } from "@codemirror/language";
import { json } from "@codemirror/lang-json";
import { tags as t } from "@lezer/highlight";

// Go text/template: plain text outside {{ }}, a small expression language
// inside. Links and #hashtags in the text are marked too.
const actionWords = /^(?:if|else|end|range|with|define|template|block|break|continue|and|or|not|eq|ne|lt|le|gt|ge|len|index|slice|print|printf|println|html|js|call)\b/;
const goTemplate = StreamLanguage.define({
  name: "gotemplate",
  startState: () => ({ inAction: false }),
  token(stream, state) {
    if (!state.inAction) {
      if (stream.match("{{")) {
        stream.match("-");
        state.inAction = true;
        return "brace";
      }
      if (stream.match(/^https?:\/\/[^\s{]+/)) return "link";
      if (stream.match(/^#[\p{L}\p{N}_]+/u)) return "atom";
      stream.next();
      while (!stream.eol() && !stream.match("{{", false) && !stream.match(/^https?:\/\//, false) && !stream.match(/^#[\p{L}\p{N}_]/u, false)) {
        stream.next();
      }
      return null;
    }
    if (stream.match(/^-?\s*}}/)) {
      state.inAction = false;
      return "brace";
    }
    if (stream.eatSpace()) return null;
    if (stream.match(/^"(?:[^"\\]|\\.)*"?/) || stream.match(/^`[^`]*`?/)) return "string";
    if (stream.match(/^-?\d+(?:\.\d+)?/)) return "number";
    if (stream.match(/^\.[\p{L}_][\p{L}\p{N}_.]*/u) || stream.match(/^\$[\p{L}\p{N}_]*/u) || stream.match(/^\.(?=\s|}|$)/)) return "variableName";
    if (stream.match(actionWords)) return "keyword";
    if (stream.match(/^[\p{L}_][\p{L}\p{N}_]*/u)) return "macroName";
    stream.next();
    return "operator";
  },
});

const highlight = HighlightStyle.define([
  { tag: t.brace, color: "var(--syn-brace)", fontWeight: "600" },
  { tag: t.keyword, color: "var(--syn-keyword)", fontWeight: "600" },
  { tag: t.macroName, color: "var(--syn-func)" },
  { tag: t.variableName, color: "var(--syn-var)" },
  { tag: t.propertyName, color: "var(--syn-var)" },
  { tag: t.string, color: "var(--syn-string)" },
  { tag: [t.number, t.bool, t.null], color: "var(--syn-number)" },
  { tag: t.link, color: "var(--syn-keyword)", textDecoration: "underline" },
  { tag: t.atom, color: "var(--syn-func)" },
  { tag: t.operator, color: "var(--syn-brace)" },
]);

const theme = EditorView.theme({
  "&": {
    backgroundColor: "var(--panel)",
    color: "var(--ink)",
    border: "1px solid var(--field)",
    borderRadius: "6px",
    fontSize: "0.9rem",
  },
  "&.cm-focused": { outline: "2px solid var(--accent)", outlineOffset: "1px" },
  ".cm-content": { fontFamily: "var(--font-mono, ui-monospace, monospace)", padding: "6px 0", caretColor: "var(--ink)" },
  ".cm-scroller": { fontFamily: "var(--font-mono, ui-monospace, monospace)", lineHeight: "1.5" },
  ".cm-gutters": { backgroundColor: "var(--bg)", color: "var(--muted)", border: "none", borderRadius: "6px 0 0 6px" },
  ".cm-activeLine": { backgroundColor: "color-mix(in srgb, var(--accent) 7%, transparent)" },
  ".cm-activeLineGutter": { backgroundColor: "transparent", color: "var(--ink)" },
  ".cm-selectionBackground, &.cm-focused .cm-selectionBackground": { backgroundColor: "color-mix(in srgb, var(--accent) 25%, transparent)" },
  ".cm-matchingBracket": { backgroundColor: "color-mix(in srgb, var(--accent) 20%, transparent)", outline: "none" },
  ".cm-placeholder": { color: "var(--muted)" },
});

// Replace each textarea[data-editor] with a code editor that keeps the
// textarea's value in step, so the form submits unchanged.
function mountEditors(nonce) {
  document.querySelectorAll("textarea[data-editor]").forEach((ta) => {
    const lang = ta.dataset.editor === "json" ? json() : goTemplate;
    const minHeight = `${Math.max(Number(ta.rows) || 4, 3) * 1.5 + 0.8}em`;
    const view = new EditorView({
      state: EditorState.create({
        doc: ta.value,
        extensions: [
          EditorView.cspNonce.of(nonce),
          lineNumbers(),
          history(),
          drawSelection(),
          highlightActiveLine(),
          bracketMatching(),
          indentOnInput(),
          EditorView.lineWrapping,
          // Tab is left alone so it moves focus on: an editor that keeps
          // Tab for indenting traps keyboard users (WCAG 2.1.2).
          keymap.of([...defaultKeymap, ...historyKeymap]),
          lang,
          syntaxHighlighting(highlight),
          theme,
          EditorView.theme({ ".cm-content, .cm-gutter": { minHeight } }),
          placeholder(ta.placeholder || ""),
          EditorView.contentAttributes.of({ "aria-label": ta.getAttribute("aria-label") || ta.name }),
          EditorView.updateListener.of((u) => {
            if (!u.docChanged) return;
            ta.value = u.state.doc.toString();
            ta.dispatchEvent(new Event("input", { bubbles: true }));
          }),
        ],
      }),
    });
    ta.hidden = true;
    ta.after(view.dom);
  });
}

// Tabs for the default body and each platform's body. Without JS every
// panel shows, stacked. With it they follow the WAI-ARIA tabs pattern: one
// tab in the focus order, arrow keys (and Home, End) move between them.
function mountTabs() {
  document.querySelectorAll("[data-tabs]").forEach((root) => {
    const tabs = [...root.querySelectorAll("[data-tab]")];
    const panels = [...root.querySelectorAll("[data-panel]")];
    const bar = root.querySelector("[data-tabbar]");
    if (bar) bar.hidden = false;
    const show = (name) => {
      tabs.forEach((b) => {
        const on = b.dataset.tab === name;
        b.setAttribute("aria-selected", String(on));
        b.tabIndex = on ? 0 : -1;
      });
      panels.forEach((p) => {
        p.hidden = p.dataset.panel !== name;
      });
    };
    tabs.forEach((b, i) =>
      b.addEventListener("keydown", (e) => {
        const to = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: tabs.length - 1 }[e.key];
        if (to === undefined) return;
        e.preventDefault();
        const next = tabs[(to + tabs.length) % tabs.length];
        show(next.dataset.tab);
        next.focus();
      }),
    );
    const markFilled = () => {
      panels.forEach((p) => {
        const ta = p.querySelector("textarea");
        const tab = tabs.find((b) => b.dataset.tab === p.dataset.panel);
        if (tab && ta && p.dataset.panel !== "default") tab.classList.toggle("has-content", ta.value.trim() !== "");
      });
    };
    tabs.forEach((b) => b.addEventListener("click", () => show(b.dataset.tab)));
    root.addEventListener("input", markFilled);
    panels.forEach((p) => p.querySelector("[data-panel-title]")?.setAttribute("hidden", ""));
    markFilled();
    show("default");
  });
}

// The live preview: renders the form on the server and shows the chosen
// platforms. The choice is remembered in this browser.
const storeKey = "araldo.preview-platforms";

function loadChoice(fallback) {
  try {
    const v = JSON.parse(localStorage.getItem(storeKey) || "null");
    if (Array.isArray(v) && v.length) return new Set(v);
  } catch {
    // Storage unavailable or corrupt: use the default.
  }
  return new Set(fallback);
}

function saveChoice(set) {
  try {
    localStorage.setItem(storeKey, JSON.stringify([...set]));
  } catch {
    // Not remembered; nothing else to do.
  }
}

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function icon(provider) {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("class", "icon");
  svg.setAttribute("aria-hidden", "true");
  const use = document.createElementNS("http://www.w3.org/2000/svg", "use");
  use.setAttribute("href", `#i-${provider}`);
  svg.appendChild(use);
  return svg;
}

function mountPreview() {
  const form = document.getElementById("template-form");
  const out = document.getElementById("preview");
  if (!form || !out) return;
  const chips = [...document.querySelectorAll("[data-preview-toggle]")];
  const names = Object.fromEntries(chips.map((c) => [c.dataset.previewToggle, c.dataset.name]));
  const chosen = loadChoice((out.dataset.defaultPlatforms || "x,facebook,instagram").split(","));
  let last = null;

  const syncChips = () => chips.forEach((c) => c.setAttribute("aria-pressed", String(chosen.has(c.dataset.previewToggle))));
  chips.forEach((c) =>
    c.addEventListener("click", () => {
      const p = c.dataset.previewToggle;
      if (chosen.has(p)) chosen.delete(p);
      else chosen.add(p);
      saveChoice(chosen);
      syncChips();
      show(last);
    }),
  );
  syncChips();

  // A short summary is announced when it changes; the preview itself is
  // not, so typing does not read every rendition aloud.
  const status = document.getElementById("preview-status");
  const announce = (text) => {
    if (status && status.textContent !== text) status.textContent = text;
  };
  const show = (res) => {
    out.replaceChildren();
    if (!res) return;
    if (res.error) {
      announce(`The template has a problem: ${res.error}`);
      const box = el("div", "alert", res.error);
      (res.problems || []).forEach((pr) => box.appendChild(el("div", "text-sm", (pr.param ? `${pr.param}: ` : "") + pr.message)));
      out.appendChild(box);
      return;
    }
    const shown = (res.renditions || []).filter((r) => chosen.has(r.provider));
    if (!shown.length) {
      out.appendChild(el("p", "muted small", "Choose platforms above to preview."));
      announce("No platforms chosen for the preview.");
      return;
    }
    const failing = shown.filter((r) => r.violations.length > 0).map((r) => names[r.provider] || r.provider);
    announce(
      failing.length === 0
        ? `All ${shown.length} platforms fit.`
        : `${failing.length} of ${shown.length} platforms have problems: ${failing.join(", ")}.`,
    );
    const grid = el("div", "preview-grid");
    shown.forEach((r) => {
      const bad = r.violations.length > 0;
      const card = el("article", `preview-card${bad ? " bad" : ""}`);
      const head = el("header", "preview-head");
      const title = el("span", "preview-title");
      title.append(icon(r.provider), el("span", "", names[r.provider] || r.provider));
      const worst = Math.max(...r.lengths);
      const count = el("span", `pill ${bad ? "bad" : "good"}`, `${worst} / ${r.limit}`);
      count.title = `${r.counting}${r.parts.length > 1 ? `, ${r.parts.length} parts` : ""}`;
      head.append(title, count);
      card.appendChild(head);
      r.parts.forEach((part, i) => {
        if (r.parts.length > 1) card.appendChild(el("div", "muted small", `Part ${i + 1} of ${r.parts.length} · ${r.lengths[i]}`));
        card.appendChild(el("pre", "post-text", part));
      });
      r.violations.forEach((v) => card.appendChild(el("p", "alert small", v.message)));
      grid.appendChild(card);
    });
    out.appendChild(grid);
  };

  let timer = null;
  let seq = 0;
  const render = () => {
    const mine = ++seq;
    const data = new FormData(form);
    data.set("csrf", out.dataset.csrf);
    fetch("/preview", { method: "POST", body: data, credentials: "same-origin" })
      .then((r) => r.json())
      .then((res) => {
        if (mine !== seq) return; // a newer render is on its way
        last = res;
        show(res);
      })
      .catch(() => {
        out.replaceChildren(el("p", "muted small", "Preview unavailable."));
      });
  };
  const soon = (ms) => {
    clearTimeout(timer);
    timer = setTimeout(render, ms);
  };
  form.addEventListener("input", () => soon(350));
  form.addEventListener("change", () => soon(50));
  render();
}

const root = document.getElementById("template-form");
if (root) {
  mountEditors(root.dataset.nonce || "");
  mountTabs();
  mountPreview();
}
