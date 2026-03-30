#!/bin/bash
set -e

# Persist all output (install + start) to a log file on the volume
LOG_DIR=/data/.gamejanitor/logs
mkdir -p "$LOG_DIR"

SESSION_LOG="$LOG_DIR/console.log"
for i in 2 1 0; do
    next=$((i + 1))
    [ -f "$SESSION_LOG.$i" ] && mv "$SESSION_LOG.$i" "$SESSION_LOG.$next"
done
[ -f "$SESSION_LOG" ] && mv "$SESSION_LOG" "$SESSION_LOG.0"

exec > >(tee "$SESSION_LOG") 2>&1

if [ "$SKIP_INSTALL" != "1" ]; then
    echo "[entrypoint] running install-server"
    /scripts/install-server
    echo "[gamejanitor:installed]"
fi

if [ -d /defaults ]; then
    cp -n /defaults/* /data/ 2>/dev/null || true
fi

# Steam games expect steamclient.so at ~/.steam/sdk64/ for networking.
# Find it in the game files (downloaded by the depot downloader) and symlink.
STEAMCLIENT=$(find /data/server -name "steamclient.so" -path "*/linux64/*" 2>/dev/null | head -1)
if [ -n "$STEAMCLIENT" ]; then
    mkdir -p /home/gameserver/.steam/sdk64
    ln -sf "$STEAMCLIENT" /home/gameserver/.steam/sdk64/steamclient.so
fi

# start-server scripts use exec, so the game binary becomes PID 1 and
# inherits the tee redirect. SIGTERM from Docker reaches it directly.
exec /scripts/start-server
