FROM ubuntu:24.04

RUN dpkg --add-architecture i386 && \
    apt-get update && \
    apt-get install -y --no-install-recommends curl wget bash procps jq ca-certificates unzip lib32gcc-s1 && \
    rm -rf /var/lib/apt/lists/*

# rcon-cli for RCON command support (used by any game with RCON)
RUN curl -fsSL https://github.com/gorcon/rcon-cli/releases/download/v0.10.3/rcon-0.10.3-amd64_linux.tar.gz | tar xz -C /tmp && \
    mv /tmp/rcon-0.10.3-amd64_linux/rcon /usr/local/bin/rcon && \
    rm -rf /tmp/rcon-0.10.3-amd64_linux

# Steam client runtime — required by most multiplayer games for Steam networking.
# Downloaded from SteamCMD's redistributable (just the shared library, not SteamCMD itself).
RUN mkdir -p /usr/lib/steam && \
    curl -fsSL https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz | tar xz -C /tmp && \
    /tmp/steamcmd.sh +quit 2>/dev/null; \
    cp /root/.local/share/Steam/steamcmd/linux64/steamclient.so /usr/lib/steam/ && \
    rm -rf /tmp/steamcmd.sh /tmp/linux32 /root/.local/share/Steam /root/.steam && \
    ldconfig

RUN useradd -m -s /bin/bash gameserver && \
    mkdir -p /data /scripts && \
    chown gameserver:gameserver /data

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

WORKDIR /data
USER gameserver

ENTRYPOINT ["/entrypoint.sh"]
