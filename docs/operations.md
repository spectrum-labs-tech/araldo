# Operating Araldo

## Configuration

Everything comes from environment variables.

| Variable | Required | Meaning |
|---|---|---|
| `ARALDO_DATABASE_URL` | yes | Postgres URL (`DATABASE_URL` also works). The role must own the schema: Araldo migrates at startup. |
| `ARALDO_MASTER_KEYS` | yes | `id:base64key[,id:base64key…]`, primary first ([ADR 0008](adr/0008-encryption.md)). Or `ARALDO_MASTER_KEYS_FILE`. |
| `ARALDO_BASE_URL` | yes | The public URL, e.g. `https://araldo.example.com`. |
| `ARALDO_LISTEN` | | HTTP address, default `:8080`. |
| `ARALDO_AUTO_MIGRATE` | | Migrate at startup, default `true`. |
| `ARALDO_CLIENT_IP_HEADER` | | Trusted proxy header with the client IP (e.g. `CF-Connecting-IP`), for sign-in rate limits. |
| `ARALDO_ALLOW_PRIVATE_NETWORKS` | | Let webhooks and adapters reach private addresses. Off by default. |
| `ARALDO_INSECURE_COOKIES` | | Plain-HTTP development only. |
| `ARALDO_LOG_LEVEL` | | `debug`, `info` (default), `warn`, `error`. |

## First run

```sh
araldo keys generate --id k1          # keep this safe, outside the database
export ARALDO_MASTER_KEYS=k1:…
araldo bootstrap --email you@example.com --org "Your org" --brand "Your product"
araldo server & araldo worker         # or: araldo all
```

`bootstrap` prints a generated password once. Sign in, then turn on
two-factor authentication under **Your account**.

## Keys

- **Back up the master keys separately from the database.** Without them,
  connected channels must be reconnected (posts and history survive).
- Rotate: generate a new key, put it first in `ARALDO_MASTER_KEYS` (keep the
  old one after it), restart, run `araldo keys rotate`, then remove the old
  key.

## Users

- `araldo users create --email …` and `araldo users reset-password --email …`
  print a generated password (or read one with `--password-stdin`).

## Health

- `GET /healthz`: the process is up.
- `GET /readyz`: the database answers and the schema is current.
- The dashboard's **Organization → Background tasks** shows every periodic
  task, its last success and failures.

## Kubernetes

The chart is in `deploy/helm/araldo` and published to
`oci://ghcr.io/spectrum-labs-tech/charts/araldo`: `0.0.0-main` follows the
main branch (its appVersion pins the exact image), and `X.Y.Z` follows
release tags. It needs `existingSecret` (a Secret with `ARALDO_DATABASE_URL`
and `ARALDO_MASTER_KEYS`) and `config.baseURL`.
