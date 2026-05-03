#!/bin/sh
set -e

# When PUID or PGID are set, modify the existing app user/group in place
# (LinuxServer.io style) so bind mounts from a NAS or rootless host work
# without manual chown. The -o flag on groupmod/usermod allows non-unique
# ids, which is what makes ids that collide with system groups (eg
# PGID=10 = wheel) work cleanly. Keeping a named user means subprocesses
# still see a valid /etc/passwd entry
if [ -n "$PUID" ] || [ -n "$PGID" ]; then
    TARGET_UID=${PUID:-1000}
    TARGET_GID=${PGID:-1000}
    groupmod -o -g "$TARGET_GID" app
    usermod -o -u "$TARGET_UID" app
    chown -R app:app /data
fi

exec su-exec app "$@"
