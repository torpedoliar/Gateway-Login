# SSO Gateway all-in-one PowerShell deploy script

**Date:** 2026-06-17
**Status:** Approved
**Author:** Claude (brainstorming → design)

## Purpose

Replace the multi-step manual flow on Windows hosts (Docker Desktop)
with a single `deploy.ps1` that takes a fresh checkout to a running
production stack. Idempotent: re-running on a working host does no
harm and only does work that's actually needed.

## Scope

**In scope:**
- `deploy/deploy.ps1` — the script itself.
- All host-side setup that today lives in `deploy/bootstrap.sh`,
  inline shell snippets in `deploy/README.md`, and the
  `docker compose …` commands an operator would type by hand.
- Generation of secrets, JWT keypair, `.env` template, log/backup
  directories.
- Preflight checks (Docker, OpenSSL, compose file, disk).
- Build, infrastructure up, interactive setup wizard, full stack up.
- Post-deploy verification (`/healthz`).

**Out of scope:**
- The setup container's internal CLI (`cmd/setup`). The script
  still invokes it interactively for the API-key prompt.
- nginx config install (still a manual step — operator-specific
  domain + cert paths).
- TLS cert issuance (Let's Encrypt / certbot).
- Update / rollback. A separate `-Update` mode is a possible
  follow-up; not in this spec.
- Linux host support. `bootstrap.sh` is kept untouched for that.

## Constraints

- PowerShell 5.1+ (Windows PowerShell default) and 7+ (pwsh) must
  both work. No Linux-only cmdlets, no `pwsh`-only syntax.
- Operator may have **no WSL, no Git Bash**. Use native
  PowerShell + cmd.exe fallbacks only where unavoidable.
- Must not require admin rights except for `chmod` on `secrets/`
  and `keys/` (which `icacls` handles, also non-admin).
- Idempotent: every action checks "is it already done?" before
  doing it. Re-running on a healthy host must be a no-op (or a
  fast verification pass).
- Fail-fast: stop at the first unrecoverable error. No
  best-effort continuation.
- All secrets stay in files, never in env vars or command line.
- The script must not depend on the host's `git` binary being on
  PATH; it assumes the checkout already exists at the cwd.

## Approach

Single script with a `-Stage` parameter (default `All`) and named
stages that can be re-run individually for debugging. The default
`All` runs all stages in order, skipping any whose work is already
done.

### Stages

| # | Stage           | Skipped when…                                          |
|---|-----------------|--------------------------------------------------------|
| 0 | Preflight       | Never (always runs; pure checks)                       |
| 1 | Secrets         | All three secret files exist + non-empty + mode 0600   |
| 2 | JWT keys        | `keys/jwt-private.pem` and `jwt-public.pem` exist      |
| 3 | `.env`          | File exists and contains non-empty `VPS_MYSQL_DSN`, `JWT_ISSUER` |
| 4 | VPS prefill     | `.setup-env` exists from a prior successful run        |
| 5 | Build           | All images already present (uses `docker compose build --pull` actually — no skip; fast on cache hit) |
| 6 | Infra up        | `postgres` and `redis` already `(healthy)`             |
| 7 | Setup wizard    | `gateway-config` volume already has `config.yaml` AND `api_keys` table is non-empty |
| 8 | Stack up        | `api` container is `(healthy)` and `sync` is running   |
| 9 | Verify          | `curl /healthz` returns 200                            |

### Stage details

**0. Preflight**
- `docker --version` exits 0.
- `docker compose version` exits 0.
- `openssl version` exits 0.
- `deploy/docker-compose.prod.yml` exists.
- Free disk ≥ 5 GB on the Docker host's data root
  (`docker info --format '{{.DockerRootDir}}'`).
- Warns (does not fail) if Docker Desktop is not running, with
  hint to start it.

**1. Secrets**
- `mkdir deploy/secrets` (existence-checked).
- `icacls deploy/secrets /inheritance:r /grant:r "$($env:USERNAME):(R,W)"` — restricts to current user.
- For each of `postgres_password.txt`, `redis_password.txt`,
  `gateway_master_key.txt` (24 / 24 / 32 bytes, base64):
  - If file exists and is non-empty: print `exists: <path>`, skip.
  - Else: write random bytes via
    `([System.Security.Cryptography.RandomNumberGenerator]::Create()).GetBytes($n)`,
    base64-encode, `Set-Content -NoNewline`.
- Set `icacls <file> /inheritance:r /grant:r "$($env:USERNAME):R"` to
  make files owner-read-only. (No group/world perms.)

**2. JWT keys**
- If `keys/jwt-private.pem` exists: skip.
- Else: run `openssl genpkey -algorithm RSA -out keys/jwt-private.pem -pkeyopt rsa_keygen_bits:2048`
  then `openssl rsa -in keys/jwt-private.pem -pubout -out keys/jwt-public.pem`.
- Restrict private key with `icacls` to current user only.

**3. `.env`**
- If `deploy/.env` exists and contains non-empty `VPS_MYSQL_DSN`
  and `JWT_ISSUER`: print `exists: deploy/.env`, skip.
- Else: write a template, then prompt the operator for those two
  values (and optional `S3_BUCKET` / `S3_ENDPOINT`).
- `VPS_MYSQL_DSN` is validated: must match
  `^[^:]+:[^@]+@tcp\(.+:\d+\)/.+\?` shape (rejects obvious typos
  before the operator goes to production).
- `.env` is `icacls`-restricted to current user.

**4. VPS prefill**
- If `deploy/.setup-env` exists: print `exists: deploy/.setup-env`,
  skip. (Operator can delete it to re-prompt.)
- Else: prompt for VPS host, port (default 3306), database (default
  sja), username (default sso_replicator), password (SecureString,
  written to file as base64 of UTF-8).
- File is `icacls`-restricted.

**5. Build**
- `docker compose -f deploy/docker-compose.prod.yml build --pull`
- Always runs. Cache hit is fast.

**6. Infra up**
- `docker compose -f deploy/docker-compose.prod.yml up -d postgres redis`
- Poll `docker compose ps` every 5 s, max 120 s, until both are
  `(healthy)`.

**7. Setup wizard**
- Skip if both `config.yaml` exists in the `gateway-config` volume
  AND `api_keys` table is non-empty (checked via
  `docker compose exec postgres psql … -c "SELECT count(*) FROM api_keys" > 0`).
- Else: `docker compose -f deploy/docker-compose.prod.yml run --rm
  --env-file deploy/.setup-env setup`. The setup binary reads the
  env vars and pre-populates prompts. Operator still sees the
  API-key prompt interactively.

**8. Stack up**
- `docker compose -f deploy/docker-compose.prod.yml up -d`
- Polls `docker compose ps` until `api` is `(healthy)` and `sync`
  is running (max 120 s).

**9. Verify**
- `curl -fsS http://localhost:8080/healthz` (internal port, since
  the stack is on the same host; nginx fronts public traffic).
- Print summary block: version, secret file sizes (sha256 first 8
  chars, not the value), api/sync/postgres/redis health, next-step
  links (nginx setup, certbot, log tail command).

### Cross-cutting

- **Logging:** all stdout/stderr also teed to
  `deploy/logs/deploy-<UTC-timestamp>.log`. A summary line is
  always written to a `deploy/logs/deploy.latest` symlink (or
  copy on Windows, which lacks symlinks without admin).
- **Colored output** via ANSI escape codes; auto-disabled if
  `$Host.UI.SupportsVirtualTerminal` is false.
- **No `Set-StrictMode`** — script is forgiving on missing
  optional fields.
- **Error action preference** is `$ErrorActionPreference = 'Stop'`
  at the top, so any unhandled throw aborts the script.

## Trade-offs

- **Re-running detection logic** is heuristic (file existence,
  healthchecks). A half-deployed state could trick the script
  into skipping a needed step. Mitigated by `-Force` switch that
  re-runs every stage regardless.
- **Polling vs. event-driven waits.** Polling is simpler and
  portable; a 5 s interval is fine for a deploy that already
  takes minutes.
- **Stage 5 always rebuilds.** `build --pull` is cheap on cache
  hit. Avoids trying to detect "is the code newer than the
  image," which is a rabbit hole.
- **Single file vs. module split.** Single file is what the
  user asked for. Internal functions only, no exports.

## Testing / verification

Manual test matrix on a Windows 11 + Docker Desktop host:

| Scenario                                       | Expected                                     |
|------------------------------------------------|----------------------------------------------|
| Fresh checkout, fresh `deploy/` dir            | All 9 stages run; full stack up              |
| Re-run after successful deploy                 | All stages skipped; only Verify runs         |
| Pre-existing secrets, no keys                  | Stage 1 skipped, Stage 2 runs                |
| `.env` exists but `VPS_MYSQL_DSN` is empty     | Stage 3 prompts to fix                       |
| Postgres fails healthcheck (port conflict)     | Stage 6 aborts with clear error              |
| Operator Ctrl-C during setup wizard            | Script exits; operator can re-run, stage 7   |
|                                                | resumes (or skips, since partial state)      |
| `-Force` on a healthy host                     | Every stage re-runs; idempotency proven      |

The script will not be unit-tested in this iteration. The
verification matrix above is the contract.

## Open questions

None. All design decisions resolved during brainstorming.

## File-level changes

- **NEW** `deploy/deploy.ps1` — the script.
- **UNCHANGED** `deploy/bootstrap.sh` — kept for Linux users.
- **UNCHANGED** `deploy/docker-compose.prod.yml` — script
  composes against the existing service definitions.
- **UNCHANGED** `cmd/setup/` — script invokes it as a black box.

## Roll-out

1. Commit `deploy/deploy.ps1`.
2. Update `deploy/README.md` to point at the new script as the
   primary install path; demote `bootstrap.sh` to "for Linux".
3. Tag a release so operators get it via `git pull`.
