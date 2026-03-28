FROM ghcr.io/warsmite/gamejanitor/base

USER root

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        openjdk-17-jre-headless \
        jq && \
    rm -rf /var/lib/apt/lists/*

USER gameserver
