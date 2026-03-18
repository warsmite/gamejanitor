FROM ghcr.io/0xkowalskidev/gamejanitor/base

USER root

RUN dpkg --add-architecture i386 && \
    apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        jq \
        lib32gcc-s1 \
        lib32stdc++6 \
        unzip \
        xmlstarlet && \
    rm -rf /var/lib/apt/lists/*

# SteamCMD
RUN mkdir -p /opt/steamcmd && \
    curl -fsSL https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz | tar xz -C /opt/steamcmd && \
    chown -R gameserver:gameserver /opt/steamcmd
ENV PATH="/opt/steamcmd:${PATH}"

# steamcmd-retry wrapper — retries on transient failures like "Missing configuration"
COPY steamcmd-retry /usr/local/bin/steamcmd-retry
RUN chmod +x /usr/local/bin/steamcmd-retry

# rcon-cli for RCON command support
RUN curl -fsSL https://github.com/gorcon/rcon-cli/releases/download/v0.10.3/rcon-0.10.3-amd64_linux.tar.gz | tar xz -C /tmp && \
    mv /tmp/rcon-0.10.3-amd64_linux/rcon /usr/local/bin/rcon && \
    rm -rf /tmp/rcon-0.10.3-amd64_linux

USER gameserver

# Pre-warm SteamCMD (first run self-updates and may exit non-zero)
RUN steamcmd.sh +quit || true
