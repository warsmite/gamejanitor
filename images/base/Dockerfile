FROM ubuntu:24.04

RUN apt-get update && \
    apt-get install -y --no-install-recommends curl wget bash procps && \
    rm -rf /var/lib/apt/lists/*

RUN useradd -m -s /bin/bash gameserver && \
    mkdir -p /data /scripts && \
    chown gameserver:gameserver /data

COPY entrypoint.sh /scripts/entrypoint.sh
COPY scripts/ /scripts/
RUN chmod +x /scripts/*

WORKDIR /data
USER gameserver

ENTRYPOINT ["/scripts/entrypoint.sh"]
