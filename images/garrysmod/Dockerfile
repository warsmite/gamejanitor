FROM registry.0xkowalski.dev/gamejanitor/base

USER root

# SteamCMD requires 32-bit libraries
RUN dpkg --add-architecture i386 && \
    apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        lib32gcc-s1 \
        lib32stdc++6 && \
    rm -rf /var/lib/apt/lists/*

# Install SteamCMD
RUN mkdir -p /opt/steamcmd && \
    curl -fsSL https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz | tar xz -C /opt/steamcmd && \
    chown -R gameserver:gameserver /opt/steamcmd
ENV PATH="/opt/steamcmd:${PATH}"

# Install rcon-cli for RCON command support
RUN curl -fsSL https://github.com/gorcon/rcon-cli/releases/download/v0.10.3/rcon-0.10.3-amd64_linux.tar.gz | tar xz -C /tmp && \
    mv /tmp/rcon-0.10.3-amd64_linux/rcon /usr/local/bin/rcon && \
    rm -rf /tmp/rcon-0.10.3-amd64_linux

USER gameserver

# Pre-install GMod server so first start only needs a delta update
# First run may self-update and exit non-zero, so run twice
RUN steamcmd.sh +quit || true && \
    steamcmd.sh \
    +force_install_dir /data/server \
    +login anonymous \
    +app_update 4020 validate \
    +quit

COPY scripts/ /scripts/

USER root
RUN chmod +x /scripts/*
USER gameserver

EXPOSE 27015/udp 27015/tcp
