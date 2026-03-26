FROM ghcr.io/warsmite/gamejanitor/base

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

USER gameserver

# Pre-warm SteamCMD (first run self-updates and may exit non-zero)
RUN steamcmd.sh +quit || true
