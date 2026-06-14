# Monitoring guide for SSO Gateway.

This service exposes Prometheus metrics at `GET /metrics` (no auth, but
ACL-restricted to internal IPs in the bundled nginx config).

## Key metrics

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `ratelimit_redis_errors_total` | counter | `op` (`script`, `reply_type`) | Redis failures during rate-limit checks. **Alert if rate > 0.1/s for 5min.** |
| `ratelimit_denied_total` | counter | – | Requests denied by the rate limiter. Expected to be small under normal load. |
| `sync_runs_rows_upserted` | derived from logs/DB | – | Number of karyawan rows upserted per sync pass. |
| `sync_runs_status` | derived from logs/DB | `status` (`success`, `failed`) | Last sync pass status. |
| `api_request_duration_seconds` | histogram (if added) | `path`, `status` | End-to-end HTTP latency. |

## Sync health queries

```sql
-- last sync status (any resource)
SELECT resource, last_status, last_run_at, last_error
FROM sync_state;

-- stale running rows (should be 0 with the startup CleanupStaleRuns)
SELECT id, resource, started_at
FROM sync_runs
WHERE status = 'running'
  AND started_at < now() - interval '1 hour';

-- last N runs
SELECT id, resource, started_at, finished_at, rows_upserted, status, error
FROM sync_runs
ORDER BY started_at DESC
LIMIT 20;
```

## Alerting rules (Prometheus)

```yaml
groups:
  - name: sso-gateway
    rules:
      # Sync hasn't succeeded in 2x the configured interval (default 10min).
      - alert: SSOSyncStale
        expr: time() - max(sync_state_last_success_timestamp) > 600
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "SSO Gateway sync has not succeeded in {{ $value | humanizeDuration }}"

      # Rate limiter bypass: redis errors are failing-open.
      - alert: SSORateLimitRedisErrors
        expr: rate(ratelimit_redis_errors_total[5m]) > 0.1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Rate limit is silently failing open due to Redis errors"

      # API container unhealthy.
      - alert: SSOAPIDown
        expr: up{job="sso-gateway-api"} == 0
        for: 2m
        labels:
          severity: critical

      # Stale 'running' sync rows.
      - alert: SSOStuckSyncRun
        expr: sync_runs_running_age_seconds > 900
        for: 0m
        labels:
          severity: warning
        annotations:
          summary: "sync_run {{ $labels.id }} stuck in running for {{ $value }}s"
```

## Backup verification

The `backup` container writes daily `pg_dump` archives to `/backupdata`.
To verify a backup is restorable:

```sh
# Copy the latest archive out of the volume
docker run --rm -v sso_backupdata:/backupdata -v $PWD:/out alpine \
  cp /backupdata/$(ls -t /backupdata | head -1) /out/

# Restore into a throwaway database
docker exec -i sso-postgres psql -U sso -d sso < /out/<archive>.sql
```

## TLS / cert renewal

The bundled nginx config assumes Let's Encrypt certs in
`/etc/letsencrypt/live/<domain>/`. Renew with certbot's standard
`--webroot` or `--nginx` plugin; nginx reload picks up the new cert
via the standard `renew_hook` (see certbot docs).
