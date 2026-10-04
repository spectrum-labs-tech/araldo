# ADR 0003: AGPL-3.0-or-later for the server; permissive licenses for clients

- Status: accepted; in effect (the DCO sign-off is not checked in CI)
- Date: 2026-09-28

## Context

Araldo will be open source under Spectrum Labs LLC, which holds the copyright
(there is no separate Araldo entity), and a hosted offering is a
possibility we want to keep open. At the same time, developers must be able
to call Araldo from closed-source products without licensing worries.

## Decision

1. **The server is AGPL-3.0-or-later.** Anyone who runs a modified Araldo as
   a network service must offer its source to its users. This is the same
   choice as open-b00ks, Plausible and Postiz.
2. **Code that runs inside callers' products is permissive:** the OpenAPI
   contract (`api/openapi.yaml`), the generated SDKs, and example
   integrations use Apache-2.0. Calling Araldo's API does not make a
   product a derivative work either way; the permissive license removes the
   doubt.
3. **Contributors sign a CLA** ([CLA.md](../../CLA.md), adapted from the
   Apache individual CLA) once, before their first contribution is merged,
   and sign off every commit under the Developer Certificate of Origin.
   Contributors keep their copyright; the CLA lets Spectrum Labs LLC offer
   Araldo under commercial terms alongside the AGPL (a hosted plan and
   commercial licenses), which without it would need every contributor's
   consent. It is in place before the repository goes public, so no
   contribution predates it.

## Alternatives considered

- **Apache-2.0 for everything.** Maximum adoption, but a
  larger company could offer hosted Araldo without contributing back.
- **Source-available licenses** (BSL, Elastic License, FSL). Not open source
  by the OSI definition, which costs trust with the developers we are
  building for.

## Consequences

- Some companies prohibit AGPL software internally. They can still use the
  API and SDKs; they cannot embed the server in a closed product.
- The file header is `// SPDX-License-Identifier: AGPL-3.0-or-later`, and CI
  checks it. The contract file and SDK repositories carry Apache-2.0.
