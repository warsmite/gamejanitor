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

# Steam games expect steamclient.so at ~/.steam/sdk32/ or sdk64/ for networking.
# Prefer the game's bundled copy, fall back to the ones in the base image.
mkdir -p /home/gameserver/.steam/sdk32 /home/gameserver/.steam/sdk64

SC64=$(find /data/server -name "steamclient.so" -path "*/linux64/*" 2>/dev/null | head -1)
[ -z "$SC64" ] && [ -f /usr/lib/steam/sdk64/steamclient.so ] && SC64=/usr/lib/steam/sdk64/steamclient.so
[ -n "$SC64" ] && ln -sf "$SC64" /home/gameserver/.steam/sdk64/steamclient.so

SC32=$(find /data/server -name "steamclient.so" -path "*/linux32/*" 2>/dev/null | head -1)
[ -z "$SC32" ] && [ -f /usr/lib/steam/sdk32/steamclient.so ] && SC32=/usr/lib/steam/sdk32/steamclient.so
[ -n "$SC32" ] && ln -sf "$SC32" /home/gameserver/.steam/sdk32/steamclient.so

# start-server scripts use exec, so the game binary becomes PID 1 and
# inherits the tee redirect. SIGTERM from Docker reaches it directly.
exec /scripts/start-server
