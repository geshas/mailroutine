#!/bin/sh
# Make the mounted /data writable for the unprivileged user, then drop root.
set -e
if [ "$(id -u)" = "0" ]; then
  mkdir -p /data
  chown mailroutine:mailroutine /data 2>/dev/null || true
  # Also fix log directory if it lives elsewhere under /data (best effort).
  exec su-exec mailroutine "$@"
fi
exec "$@"
