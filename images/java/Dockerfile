FROM registry.0xkowalski.dev/gamejanitor/base

USER root

RUN apt-get update && \
    apt-get install -y --no-install-recommends openjdk-21-jre-headless jq && \
    rm -rf /var/lib/apt/lists/*

# TODO: revisit multi-version Java strategy when more MC versions need Java 25+.
# Add JDK 25 here when available, and have start-server scripts select the right one.

USER gameserver
