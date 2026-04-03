FROM ubuntu:24.04

RUN dpkg --add-architecture i386 && \
    apt-get update && \
    apt-get install -y --no-install-recommends curl wget bash procps jq ca-certificates unzip lib32gcc-s1 lib32stdc++6 strace && \
    rm -rf /var/lib/apt/lists/*

# rcon-cli for RCON command support (used by any game with RCON)
RUN curl -fsSL https://github.com/gorcon/rcon-cli/releases/download/v0.10.3/rcon-0.10.3-amd64_linux.tar.gz | tar xz -C /tmp && \
    mv /tmp/rcon-0.10.3-amd64_linux/rcon /usr/local/bin/rcon && \
    rm -rf /tmp/rcon-0.10.3-amd64_linux

# Steam client runtime — required by most multiplayer games for Steam networking.
# Downloaded from SteamCMD's redistributable (just the shared library, not SteamCMD itself).
# Steam client runtime — required by most multiplayer games for Steam networking.
# Both 32-bit and 64-bit versions are needed (e.g. GMod is 32-bit, Rust is 64-bit).
RUN mkdir -p /usr/lib/steam/sdk32 /usr/lib/steam/sdk64 /tmp/steamcmd && \
    curl -fsSL https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz | tar xz -C /tmp/steamcmd && \
    /tmp/steamcmd/steamcmd.sh +quit 2>/dev/null; true && \
    find / -name "steamclient.so" -path "*/linux32/*" 2>/dev/null -exec cp {} /usr/lib/steam/sdk32/ \; && \
    find / -name "steamclient.so" -path "*/linux64/*" 2>/dev/null -exec cp {} /usr/lib/steam/sdk64/ \; && \
    rm -rf /tmp/steamcmd /root/.local/share/Steam /root/.steam && \
    ldconfig

COPY run-server /usr/local/bin/run-server
RUN chmod +x /usr/local/bin/run-server

RUN useradd -m -s /bin/bash gameserver && \
    mkdir -p /data /scripts /defaults /depot && \
    chown gameserver:gameserver /data

WORKDIR /data
USER gameserver
