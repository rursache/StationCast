#!/bin/sh
set -e

# When PUID or PGID are set, run the binary as that numeric uid:gid via
# su-exec and chown /data so bind mounts from a NAS or rootless host work
# without manual chown. su-exec accepts numeric ids directly so we do not
# need to mutate /etc/passwd or /etc/group, which sidesteps clashes with
# existing system groups (eg PGID=10 = wheel)
if [ -n "$PUID" ] || [ -n "$PGID" ]; then
    TARGET_UID=${PUID:-1000}
    TARGET_GID=${PGID:-1000}
    chown -R "$TARGET_UID:$TARGET_GID" /data
    exec su-exec "$TARGET_UID:$TARGET_GID" "$@"
fi

exec su-exec app "$@"
