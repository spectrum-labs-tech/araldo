# ADR 0015: A server-rendered dashboard embedded in the binary

- Status: accepted; built, except the calendar and the request log
- Date: 2026-09-28

## Context

The API is the product, but people still need a dashboard to sign in, set up
MFA, connect channels (OAuth needs a browser), edit templates with a live
preview, approve posts, manage keys and webhooks, and see what happened.
Self-hosting should remain "one binary and a Postgres".

## Decision

1. **Server-rendered HTML** (`html/template`) with a little hand-written
   JavaScript only where a page needs it (template preview), styled with
   **Tailwind CSS**: utilities for layout in the templates, and component
   classes in `internal/web/styles/app.css` for anything repeated (shell,
   buttons, cards, pills, fields). Colors are semantic tokens (`bg-panel`,
   `text-muted`) that switch with the system's light or dark preference.
2. **Embedded in the binary** (`embed.FS`) and served by `araldo server`
   next to the API, on the same origin.
3. **The dashboard calls services directly**, never the HTTP API, and uses
   session cookies with CSRF tokens ([ADR 0007](0007-authentication-and-mfa.md)).
   The API uses keys only. This keeps each surface's authentication simple.
4. **Node is build tooling only.** Tailwind comes from npm
   (`package.json`); `task web:css` compiles the stylesheet into
   `internal/web/static/app.css`, which is committed and embedded in the
   binary. Nobody needs Node to build or run Araldo unless they change
   styles, and CI fails if the committed output is stale. Application code
   is Go, never JavaScript beyond the small progressive-enhancement script.
5. **Accessibility:** WCAG 2.2 AA, checked by tests: `TestContrast`
   measures the theme's colors (light and dark) and `TestPagesAreAccessible`
   renders every page and checks names, alt text, headings, landmarks, the
   skip link, references and that nothing submits on input. Forms work
   without JavaScript.
   JavaScript only enhances: `static/app.js` is hand-written, and the
   template editor (CodeMirror, syntax highlighting, per-platform tabs, the
   live preview) is bundled from `internal/web/scripts` with esbuild into a
   committed `static/editor.js` that only that page loads. The dashboard's
   Content-Security-Policy allows no inline code; the editor's injected
   styles carry a per-request nonce.
6. **First screens:**
   - sign-in and MFA;
   - org, brand and member settings;
   - connecting channels;
   - API keys, and webhooks with their delivery log;
   - the template editor with a per-provider preview;
   - the queue and calendar, and approvals;
   - events and the request log.

## Alternatives considered

- **Hand-written CSS** (the first version). Fine for a dozen pages, but it
  drifts as screens are added, and contributors know Tailwind.
- **A SvelteKit single-page app** (open-b00ks). A second codebase, a Node
  build, and an API surface that would have to accept cookies as well as
  keys.
- **Dashboard as a pure API client.** Tidy in theory, but every screen
  would need public endpoints first, including sensitive ones such as MFA
  enrollment that do not belong in the public API.

## Consequences

- One image, one origin, one set of sessions.
- Highly interactive pieces (for example a drag-and-drop calendar) will need
  some hand-written JavaScript; if that grows large, revisit.
