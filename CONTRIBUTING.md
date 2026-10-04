# Contributing to Araldo

Thanks for helping. Araldo exists so that any product can announce itself
everywhere its users are, from code, without a marketing team.

## Getting set up

You need [Go](https://go.dev/dl/) (version in `go.mod`),
[Task](https://taskfile.dev), and Docker for the integration tests.

```sh
task            # vet + unit tests
task check      # everything CI checks without Docker
task db:up && task test:integration
```

Changing templates or styles? You also need [Node.js](https://nodejs.org)
20 or later, only to compile the stylesheet (it is committed, so nobody else
needs Node):

```sh
task web:css     # rebuild internal/web/static/app.css; commit it with your change
task web:watch   # rebuild on every save while you work
```

## Ground rules

1. **Read the ADRs** in [docs/adr](docs/adr/) before changing a behavior
   they describe. To change a decision, propose a new ADR.
2. **No real credentials** in issues, pull requests, tests or logs. Platform
   adapters are tested against local `httptest` servers only.
3. **Tests come with changes.** Tenant-facing operations need a test that
   calls them as another org and expects "not found".

## Sign the CLA once

Araldo is stewarded by Spectrum Labs LLC. Before your first pull request can
be merged, you sign the [Contributor License Agreement](CLA.md): a bot
comments on the pull request, and you reply with the sentence it gives you.
You sign once, for all future contributions.

You keep the copyright to your work. The CLA lets Spectrum Labs distribute
Araldo under the AGPL and also offer it under separate commercial terms,
which is how the project pays for itself.

## Sign off your commits

Also add a `Signed-off-by` line to every commit (`git commit -s`). Under the
[Developer Certificate of Origin](https://developercertificate.org/), it
certifies that you wrote the change or otherwise have the right to submit
it.
