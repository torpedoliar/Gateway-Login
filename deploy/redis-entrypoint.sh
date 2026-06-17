#!/bin/sh
# Redis entrypoint: materialize a runtime redis.conf from the Docker
# secret so the password is set without being baked into the image or
# visible in `docker inspect`. Runs once per container start.
set -eu

umask 077

if [ ! -r /run/secrets/redis_password ]; then
  echo "FATAL: /run/secrets/redis_password missing or unreadable" >&2
  exit 1
fi

# Strip trailing CR/LF that Windows editors tend to leave in secret files.
PASSWORD=$(tr -d '\n\r' < /run/secrets/redis_password)
if [ -z "$PASSWORD" ]; then
  echo "FATAL: /run/secrets/redis_password is empty" >&2
  exit 1
fi

# Write runtime config to tmpfs (/tmp). Not persisted; rebuilt on restart.
{
  echo "# Auto-generated at startup; do not edit."
  echo "bind 0.0.0.0 -::*"
  echo "protected-mode yes"
  echo "requirepass ${PASSWORD}"
  echo "save 60 100"
  echo "appendonly yes"
} > /tmp/redis.conf

chmod 600 /tmp/redis.conf
unset PASSWORD

exec redis-server /tmp/redis.conf
