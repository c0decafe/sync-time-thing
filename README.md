# SyncTimeThing

SyncTimeThing is a lightweight self-hosted web application for defining time-based pause and resume rules against a Syncthing instance through the Syncthing admin API.

## Highlights

- single binary Go service
- server-rendered UI with minimal static assets
- SQLite persistence with a pure-Go driver
- SQLite timestamps, indexes, and session cleanup tuned for stable ordering and low-maintenance operation
- cron-style scheduling with one app-wide timezone
- global, device, and folder pause/resume rules
- production container built through `devenv container build prod`

## Architecture

- `cmd/sync-time-thing`: tiny executable entrypoint
- `internal/config`: environment loading and validation
- `internal/auth`: password hashing and session token helpers
- `internal/cronexpr`: 5-field cron parser and scheduler helpers
- `internal/store`: SQLite persistence
- `internal/syncthing`: Syncthing admin API client
- `internal/scheduler`: due-rule evaluation and execution orchestration
- `internal/web`: server-rendered HTTP UI
- `internal/app`: application wiring and lifecycle

## Development

```bash
devenv shell
cp .env.example .env
export SYNCTIMETHING_ADMIN_PASSWORD='choose-a-real-password'
synctimething-run
```

Useful commands inside the devenv shell:

- `devenv processes up`
- `devenv processes wait`
- `synctimething-run`
- `synctimething-app-run`
- `synctimething-app-reset`
- `synctimething-test`
- `synctimething-smoke`
- `synctimething-smoke-check`
- `synctimething-e2e`
- `synctimething-e2e-check`
- `synctimething-check`
- `synctimething-build`
- `synctimething-fmt`
- `synctimething-syncthing-reset`
- `synctimething-syncthing-run`

The harness commands use isolated state under `.devenv/state/`:

- the app harness stores data in `.devenv/state/harness-app`
- the Syncthing harness stores config in `.devenv/state/harness-syncthing`
- the Syncthing harness keeps its default folder inside `.devenv/state/harness-syncthing-home/Sync`
- the local Syncthing GUI/API endpoint defaults to `http://127.0.0.1:18484`
- the local Syncthing GUI API key defaults to `synctimething-dev-syncthing-key`

`devenv test` now uses native `devenv` processes for the local Syncthing harness and app harness, with readiness checks and lifecycle reset tasks. The standalone `synctimething-smoke` and `synctimething-e2e` commands remain available for one-shot manual runs, while `synctimething-smoke-check` and `synctimething-e2e-check` assume the harness processes are already up.

## Configuration

| Variable | Description | Default |
| --- | --- | --- |
| `SYNCTIMETHING_LISTEN_ADDR` | HTTP listen address | `:8080` |
| `SYNCTIMETHING_DATA_DIR` | Directory holding the SQLite database | `.devenv/state/data` in devenv |
| `SYNCTIMETHING_DB_PATH` | Explicit SQLite path override | derived from `SYNCTIMETHING_DATA_DIR` |
| `SYNCTIMETHING_TIMEZONE` | App-wide IANA timezone | `UTC` |
| `SYNCTIMETHING_SESSION_TTL` | Session duration | `24h` |
| `SYNCTIMETHING_SECURE_COOKIES` | Whether auth cookies require HTTPS | `false` |
| `SYNCTIMETHING_ADMIN_USERNAME` | Bootstrap admin username | `admin` |
| `SYNCTIMETHING_ADMIN_PASSWORD` | Bootstrap admin password; required on first boot | none |
| `SYNCTIMETHING_ENCRYPTION_KEY` | Base64-encoded 32-byte key used to encrypt stored Syncthing API credentials at rest | none |
| `SYNCTIMETHING_RULE_RUN_RETENTION` | How long to retain `rule_runs` history; `0` disables pruning | `2160h` |

If `SYNCTIMETHING_ADMIN_PASSWORD` is set on a later boot, the stored admin password is rotated to the new value.

If you want to save Syncthing credentials in the UI, `SYNCTIMETHING_ENCRYPTION_KEY` must be set first. Plaintext Syncthing API keys stored in the database are intentionally unsupported; if one is present, clear it and re-save settings after configuring the encryption key.

## Storage details

The SQLite store is configured for a small single-process deployment:

- WAL mode with foreign keys and a busy timeout
- fixed-width UTC timestamp encoding so recent-run ordering stays stable
- an index for recent run history and an index for expired-session cleanup
- automatic pruning of expired sessions during session creation and expired-session lookups
- AES-GCM encryption at rest for the stored Syncthing API key
- configurable pruning of old `rule_runs` rows so history stays bounded over time
- `PRAGMA optimize` after migrations

For backup/restore, treat the mounted data directory as the persistence boundary. Back up the SQLite database directory regularly, and for simple cold backups stop the container first so the main database, WAL, and SHM state stay consistent together.

## Production container

Build the production image with `devenv`:

```bash
devenv container build prod
```

That produces an OCI image artifact derived from the same declarative environment used for local development.

A typical Docker deployment should mount a persistent volume for the SQLite database directory and pass the bootstrap/admin environment variables.

The production image is built through `devenv container build prod`, but the `prod` image derivation is intentionally minimal: it ships only the scheduler binary, CA certificates, tzdata, and the few runtime directories the app needs.

The refreshed OCI output now imports into Docker at about `52.6 MB`, uses `/bin/sync-time-thing` as its direct entrypoint, defaults `SYNCTIMETHING_DATA_DIR` to `/data`, and was re-verified locally by starting the container and fetching `http://127.0.0.1:18081/login`.

### Run from GHCR

```bash
docker run -d --name sync-time-thing --restart unless-stopped -p 8080:8080 -v sync-time-thing-data:/data -e SYNCTIMETHING_ADMIN_PASSWORD='change-me-now' -e SYNCTIMETHING_ENCRYPTION_KEY="$(openssl rand -base64 32)" ghcr.io/c0decafe/sync-time-thing:latest
```

Keep the generated `SYNCTIMETHING_ENCRYPTION_KEY` stable for that deployment if you want stored Syncthing credentials to remain decryptable across container re-creates.

## GitHub Actions publishing

The repository includes `.github/workflows/publish-ghcr.yml` to build the `prod` container with `devenv` and push it to GHCR.

- default branch pushes publish:
  - `ghcr.io/c0decafe/sync-time-thing:latest`
  - `ghcr.io/c0decafe/sync-time-thing:sha-<12-char-commit>`
- version tags like `v1.2.3` also publish:
  - `v1.2.3`
  - `1.2.3`
  - `1.2`
  - `1`

The workflow uses the repository `GITHUB_TOKEN` with `packages: write`, installs Nix, enables Nix binary caching, and pushes via `devenv container copy`.

## Testing

The repository is designed around exhaustive unit coverage. Run:

```bash
synctimething-test
```

For the higher-level harness checks:

```bash
synctimething-smoke   # boots the app and verifies health/login/dashboard
synctimething-e2e     # boots local Syncthing + app, saves settings, creates a rule, and verifies execution
synctimething-smoke-check
synctimething-e2e-check
synctimething-check   # runs unit coverage, smoke, and E2E together
devenv test       # same as synctimething-check via enterTest
```

`synctimething-test` still enforces `100.0%` aggregate Go coverage. `devenv test` now uses `devenv`-managed harness processes plus the smoke/E2E check scripts, while `synctimething-smoke` and `synctimething-e2e` continue to provide standalone shell-driven end-to-end runs.
