FROM registry.0xkowalski.dev/gamejanitor/base

USER root
RUN apt-get update && \
    apt-get install -y --no-install-recommends unzip jq ca-certificates libicu74 && \
    rm -rf /var/lib/apt/lists/*

# TShock requires .NET 9 runtime (not in Ubuntu 24.04 repos)
RUN curl -fsSL https://dot.net/v1/dotnet-install.sh -o /tmp/dotnet-install.sh && \
    bash /tmp/dotnet-install.sh --channel 9.0 --runtime dotnet --install-dir /usr/share/dotnet && \
    ln -s /usr/share/dotnet/dotnet /usr/bin/dotnet && \
    rm /tmp/dotnet-install.sh
USER gameserver

COPY scripts/ /scripts/

USER root
RUN chmod +x /scripts/*
USER gameserver

EXPOSE 7777 7878
