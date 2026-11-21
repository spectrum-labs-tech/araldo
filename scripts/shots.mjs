// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Photographs every dashboard page at phone and desktop width, light and
// dark, into tmp/shots/, and runs axe on each (task web:shots). Driven by
// internal/web/shots_integration_test.go, which seeds the data and passes
// a plan: {base, token, pages}. Uses the installed Chrome (or Edge), so
// nothing downloads a browser. SHOTS=keys,webhooks limits it to paths
// containing any of those words.
import { readFileSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import { chromium } from "playwright-core";
import axe from "axe-core";

const plan = JSON.parse(readFileSync(process.argv[2], "utf8"));
const only = (process.env.SHOTS || "").split(",").filter(Boolean);
const pages = plan.pages.filter((p) => !only.length || only.some((w) => p.includes(w)));
const out = "tmp/shots";
if (!only.length) rmSync(out, { recursive: true, force: true });
mkdirSync(out, { recursive: true });

const sizes = [
  { name: "phone", width: 390, height: 844 },
  { name: "desktop", width: 1280, height: 900 },
];
const schemes = ["light", "dark"];

async function launch() {
  for (const channel of ["chrome", "msedge"]) {
    try {
      return await chromium.launch({ channel });
    } catch {}
  }
  throw new Error("no Chrome or Edge installed for playwright-core to drive");
}

// slug names a page's files: /posts?q=x → posts_q-x.
const slug = (p) => (p.replace(/^\//, "").replace(/[^a-z0-9]+/gi, "-").replace(/^-|-$/g, "") || "home").slice(0, 80);

const browser = await launch();
const report = [];
let failures = 0;
for (const scheme of schemes) {
  for (const size of sizes) {
    // bypassCSP lets axe's script run under the dashboard's nonce policy.
    const ctx = await browser.newContext({ viewport: size, colorScheme: scheme, bypassCSP: true });
    const { hostname } = new URL(plan.base);
    await ctx.addCookies([{ name: "araldo_session", value: plan.token, domain: hostname, path: "/" }]);
    const page = await ctx.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    // A failed load is reported by URL below, so the seeded data's
    // made-up hosts (.test, .example) can be told apart from real faults.
    page.on("console", (m) => m.type() === "error" && !m.text().startsWith("Failed to load resource") && errors.push(m.text()));
    page.on("requestfailed", (r) => !/\.(test|example)(:\d+)?$/.test(new URL(r.url()).hostname) && errors.push(`failed: ${r.url()}`));
    page.on("response", (r) => r.status() >= 400 && r.url().startsWith(plan.base) && errors.push(`${r.status()}: ${r.url()}`));
    for (const path of pages) {
      errors.length = 0;
      const res = await page.goto(plan.base + path, { waitUntil: "networkidle" });
      const file = `${out}/${slug(path)}.${size.name}.${scheme}.png`;
      await page.screenshot({ path: file, fullPage: true });
      const entry = { path, size: size.name, scheme, status: res?.status(), file, errors: [...errors], violations: [] };
      if ((res?.status() ?? 0) >= 400) failures++;
      // axe once per scheme (contrast differs), at both widths (reflow does).
      await page.evaluate(axe.source);
      const r = await page.evaluate(() => window.axe.run(document, { runOnly: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa", "best-practice"] }));
      entry.violations = r.violations.map((v) => ({ id: v.id, impact: v.impact, help: v.help, nodes: v.nodes.map((n) => n.target.join(" ")).slice(0, 5) }));
      // A horizontal scroll on a phone is a reflow failure (WCAG 1.4.10).
      if (size.name === "phone") {
        // Names the outermost elements that run past the window, skipping
        // what scrolls inside itself.
        const wide = await page.evaluate(() => {
          if (document.documentElement.scrollWidth <= window.innerWidth + 1) return null;
          const name = (el) => el.tagName.toLowerCase() + (el.id ? "#" + el.id : "") + (el.className && typeof el.className === "string" ? "." + el.className.trim().split(/\s+/).slice(0, 3).join(".") : "");
          const out = [];
          const walk = (el) => {
            for (const c of el.children) {
              const st = getComputedStyle(c);
              if (c.getBoundingClientRect().right > window.innerWidth + 1 && st.position !== "fixed") {
                const inner = [...c.children].some((g) => g.getBoundingClientRect().right > window.innerWidth + 1);
                if (!inner || st.overflowX === "auto") out.push(name(el) + " > " + name(c));
                else walk(c);
              } else if (st.overflowX !== "auto" && st.overflowX !== "hidden") walk(c);
            }
          };
          walk(document.body);
          return out.slice(0, 5);
        });
        if (wide) entry.violations.push({ id: "reflow", impact: "serious", help: "The page scrolls sideways at 390px", nodes: wide });
      }
      failures += entry.violations.length + entry.errors.length;
      report.push(entry);
      const flag = entry.violations.length || entry.errors.length ? "✗" : "✓";
      console.log(`${flag} ${size.name} ${scheme} ${path}${entry.violations.map((v) => `\n    ${v.impact} ${v.id}: ${v.help} ${v.nodes.join(", ")}`).join("")}${entry.errors.map((e) => `\n    console: ${e}`).join("")}`);
    }
    await ctx.close();
  }
}
await browser.close();
writeFileSync(`${out}/report.json`, JSON.stringify(report, null, 2));
console.log(`\n${report.length} shots in ${out}/, ${failures} problem(s); details in ${out}/report.json`);
process.exit(failures ? 1 : 0);
