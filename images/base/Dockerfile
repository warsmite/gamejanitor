FROM ubuntu:24.04

RUN apt-get update && \
    apt-get install -y --no-install-recommends curl wget bash procps && \
    rm -rf /var/lib/apt/lists/*

RUN useradd -m -s /bin/bash gameserver && \
    mkdir -p /data /scripts && \
    chown gameserver:gameserver /data

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

WORKDIR /data
USER gameserver

ENTRYPOINT ["/entrypoint.sh"]
