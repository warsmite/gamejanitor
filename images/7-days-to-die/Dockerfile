FROM registry.0xkowalski.dev/gamejanitor/base

USER root

RUN dpkg --add-architecture i386 && \
    apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        lib32gcc-s1 \
        lib32stdc++6 \
        xmlstarlet && \
    rm -rf /var/lib/apt/lists/*

# Install SteamCMD
RUN mkdir -p /opt/steamcmd && \
    curl -fsSL https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz | tar xz -C /opt/steamcmd && \
    chown -R gameserver:gameserver /opt/steamcmd
ENV PATH="/opt/steamcmd:${PATH}"

USER gameserver

# Pre-install 7D2D server so first start only needs a delta update
# First run self-updates SteamCMD; second run installs the game
RUN steamcmd.sh +quit || true
RUN steamcmd.sh \
    +force_install_dir /data/server \
    +login anonymous \
    +app_update 294420 validate \
    +quit

COPY scripts/ /scripts/

USER root
RUN chmod +x /scripts/*
USER gameserver

EXPOSE 26900/tcp 26900/udp
