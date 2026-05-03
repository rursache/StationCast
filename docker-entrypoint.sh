#!/bin/sh
set -e

# When PUID or PGID are set, recreate the app user/group with those ids and
# fix ownership on the data dir so bind mounts from a NAS or rootless host
# work without manual chown. Without these vars, the baked-in app user runs
# as before
if [ -n "$PUID" ] || [ -n "$PGID" ]; then
    TARGET_UID=${PUID:-1000}
    TARGET_GID=${PGID:-1000}

    current_gid=$(getent group app | cut -d: -f3 || true)
    if [ "$current_gid" != "$TARGET_GID" ]; then
        delgroup app 2>/dev/null || true
        addgroup -g "$TARGET_GID" app
    fi

    current_uid=$(id -u app 2>/dev/null || true)
    if [ "$current_uid" != "$TARGET_UID" ]; then
        deluser app 2>/dev/null || true
        adduser -u "$TARGET_UID" -G app -D -H -h /app app
    fi

    chown -R app:app /data
fi

exec su-exec app "$@"
