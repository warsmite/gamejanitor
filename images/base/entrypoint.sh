#!/bin/bash
set -e

if [ ! -f /data/.installed ]; then
    echo "[entrypoint] running install-server"
    /scripts/install-server
    touch /data/.installed
fi

if [ -d /defaults ]; then
    cp -n /defaults/* /data/ 2>/dev/null || true
fi

/scripts/start-server &
SERVER_PID=$!
wait "$SERVER_PID"
