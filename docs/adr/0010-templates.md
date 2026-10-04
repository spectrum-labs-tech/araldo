# ADR 0010: Templates are versioned Go text/templates with a JSON Schema and per-platform bodies

- Status: accepted; built, except the per-version `media` field and warnings on save
- Date: 2026-09-28

## Context

Products announce the same kinds of things again and again: a featured
item, a new release, a weekly digest. The
caller knows the facts (name, image, link); the wording, and how it
differs between X and LinkedIn, belongs to whoever runs the brand, and
should change without redeploying the caller.

## Decision

1. **A template belongs to a brand** and has a `key` that is unique within
   the brand (`featured-build`). Callers refer to `featured-build` (the
   latest published version) or `featured-build@3`.
2. **Versions are immutable.** Saving a template makes a new version, and
   the preview endpoint is where drafts are tried out. A post records the
   exact version it was rendered from.
3. **A template version contains:**
   - `variables`: a JSON Schema (draft 2020-12) for the data the caller
     sends, validated with `santhosh-tekuri/jsonschema`. It must include
     `examples`, which are used for validation and previews.
   - `body`: the default body, plus optional per-provider overrides
     (`x`, `linkedin`, …).
   - `media`: which variables hold media (a URL or a `media_…` ID) and alt
     text.
   - `fit`, per provider: what to do with a body that is too long:
     `error` (default), `truncate` (at a word boundary, with an ellipsis),
     or `thread` (split into a thread where the provider supports it).
4. **The language is Go's `text/template`**, with a fixed set of helper
   functions: `truncate`, `words`, `join`, `hashtags`, `lower`, `upper`,
   `default`, `plural`, `date` (in the brand's time zone), `urlquery`.
   Data is plain JSON (`map[string]any`), so templates can reach no methods
   or anything beyond the caller's data. Template source is limited to
   16 KiB and data to 64 KiB.
5. **Validation happens early:**
   - **On save:** every body must parse; every body is rendered with each
     example for every provider the brand has channels on; rule violations
     are returned as warnings.
   - **On post creation:** each target is rendered and checked against its
     provider's rules; a violation that `fit` cannot fix is a 422 listing
     every target's problems.
   - **The rendered text is stored on the target at creation** and never
     rendered again, so what the caller previewed is what is published.
6. **Preview without side effects:** `POST /v1/templates/{key}/preview` and
   `POST /v1/posts/preview` return the rendered text per target and every
   violation with a stable `code` (`too_long`, `media_required`,
   `variable_missing`, …), the measured length and the limit.
7. **Templates are optional.** A post may instead carry
   `content.body` and `content.overrides[provider]` written by the caller
   (often an AI agent); the same validation, `fit` and preview apply.

## Alternatives considered

- **Liquid or Handlebars syntax.** More familiar to JavaScript developers,
  but it means a third-party engine and its security record; `text/template`
  is in the standard library and familiar to Go developers. Revisit if
  users struggle with the syntax.
- **Mustache.** Logic-less is attractive, but it cannot truncate, pluralize
  or format dates, which social copy needs constantly.
- **Rendering at publish time.** Would pick up template fixes for queued
  posts, but previews could then differ from what is published; freezing
  the text is easier to trust.

## Consequences

- JSON Schema validation adds one dependency.
- A template change never alters an already queued post; a caller that
  wants the new wording cancels the post and creates it again.
- The dashboard's template editor can show a live preview per provider
  using the same endpoint.
