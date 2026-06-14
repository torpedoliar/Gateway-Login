# Secrets directory

Each file in this directory is mounted into the corresponding service
as a Docker secret (path `/run/secrets/<basename>`).

| File | Used by | Length | Mode |
|---|---|---|---|
| `postgres_password.txt` | postgres, api, sync, setup, backup | 24 bytes | 0600 |
| `redis_password.txt` | redis, api | 24 bytes | 0600 |
| `gateway_master_key.txt` | api, sync | 32 bytes (base64) | 0600 |

## Generate / rotate

```sh
./deploy/bootstrap.sh   # generates only if absent
```

Manual rotation (e.g. compromised credential):

```sh
# 1. Generate new secret
head -c 24 /dev/urandom | base64 > deploy/secrets/postgres_password.txt
chmod 600 deploy/secrets/postgres_password.txt

# 2. Update the corresponding service env if it uses the env form
#    (this stack uses the *_FILE form, so no env update needed)

# 3. Restart the affected services
docker compose -f deploy/docker-compose.prod.yml up -d
```

## What never belongs here

- VPS MySQL password — that lives encrypted at rest in
  `config.yaml` (via AES-256-GCM with the master key).
- API keys — those are in the `api_keys` table.
- TLS certs — those belong in `/etc/letsencrypt/live/<domain>/`.
