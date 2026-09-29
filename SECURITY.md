# Security policy

Araldo holds credentials that can post as other people's accounts. We take
reports seriously and appreciate responsible disclosure.

## Reporting a vulnerability

**Do not open a public issue.** Report privately through GitHub's
[private vulnerability reporting](https://github.com/spectrum-labs-tech/araldo/security/advisories/new).

Please include the affected version or commit, a description of the impact,
and steps to reproduce. Never include real platform credentials.

We aim to acknowledge reports within 3 business days and to agree on a fix
and disclosure timeline within 10 business days.

## Supported versions

Until 1.0, only the latest release receives security fixes.

## Operator responsibilities

Araldo is software you run. Operators are responsible for TLS, keeping the
master keys ([ADR 0008](docs/adr/0008-encryption.md)) outside the database
and backed up, database encryption at rest, and network controls.
