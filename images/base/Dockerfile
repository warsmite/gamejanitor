FROM ubuntu:24.04

RUN apt-get update && \
    apt-get install -y --no-install-recommends curl wget bash procps jq ca-certificates unzip && \
    rm -rf /var/lib/apt/lists/*

# rcon-cli for RCON command support (used by any game with RCON)
RUN curl -fsSL https://github.com/gorcon/rcon-cli/releases/download/v0.10.3/rcon-0.10.3-amd64_linux.tar.gz | tar xz -C /tmp && \
    mv /tmp/rcon-0.10.3-amd64_linux/rcon /usr/local/bin/rcon && \
    rm -rf /tmp/rcon-0.10.3-amd64_linux

RUN useradd -m -s /bin/bash gameserver && \
    mkdir -p /data /scripts && \
    chown gameserver:gameserver /data

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

WORKDIR /data
USER gameserver

ENTRYPOINT ["/entrypoint.sh"]
