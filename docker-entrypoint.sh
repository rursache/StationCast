#!/bin/sh
set -e

# Always set the in-image app user/group ids to PUID/PGID (default 1000:1000)
# so bind mounts from a NAS or rootless host work without manual chown. The
# -o flag on groupmod/usermod allows non-unique ids, which lets values that
# collide with system groups (eg PGID=10 = wheel) work cleanly. Keeping a
# named user means subprocesses still see a valid /etc/passwd entry
TARGET_UID=${PUID:-1000}
TARGET_GID=${PGID:-1000}
groupmod -o -g "$TARGET_GID" app
usermod -o -u "$TARGET_UID" app
chown -R app:app /data

exec su-exec app "$@"
