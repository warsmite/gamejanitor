FROM registry.0xkowalski.dev/gamejanitor/base

USER root
RUN apt-get update && \
    apt-get install -y --no-install-recommends openjdk-21-jre-headless jq && \
    rm -rf /var/lib/apt/lists/*
USER gameserver

COPY scripts/ /scripts/
COPY defaults/ /defaults/

USER root
RUN chmod +x /scripts/*
USER gameserver

EXPOSE 25565
