FROM ghcr.io/0xkowalskidev/gamejanitor/base

USER root

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        openjdk-8-jre-headless \
        openjdk-17-jre-headless \
        openjdk-21-jre-headless \
        openjdk-25-jre-headless \
        jq && \
    rm -rf /var/lib/apt/lists/*

USER gameserver
