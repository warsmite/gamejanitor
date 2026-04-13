FROM ubuntu:24.04

RUN dpkg --add-architecture i386 && \
    apt-get update && \
    apt-get install -y --no-install-recommends \
        curl wget bash procps jq ca-certificates unzip strace \
        lib32gcc-s1 lib32stdc++6 libicu74 \
        openjdk-8-jre-headless \
        openjdk-17-jre-headless \
        openjdk-21-jre-headless \
        openjdk-25-jre-headless && \
    rm -rf /var/lib/apt/lists/*

# rcon-cli for RCON command support (used by any game with RCON)
RUN curl -fsSL https://github.com/gorcon/rcon-cli/releases/download/v0.10.3/rcon-0.10.3-amd64_linux.tar.gz | tar xz -C /tmp && \
    mv /tmp/rcon-0.10.3-amd64_linux/rcon /usr/local/bin/rcon && \
    rm -rf /tmp/rcon-0.10.3-amd64_linux

# .NET 9 runtime (not in Ubuntu 24.04 repos, installed via official script)
RUN curl -fsSL https://dot.net/v1/dotnet-install.sh -o /tmp/dotnet-install.sh && \
    bash /tmp/dotnet-install.sh --channel 9.0 --runtime dotnet --install-dir /usr/share/dotnet && \
    ln -s /usr/share/dotnet/dotnet /usr/bin/dotnet && \
    rm /tmp/dotnet-install.sh

# Steam client runtime — required by most multiplayer games for Steam networking.
# Both 32-bit and 64-bit versions are needed (e.g. GMod is 32-bit, Rust is 64-bit).
RUN mkdir -p /usr/lib/steam/sdk32 /usr/lib/steam/sdk64 /tmp/steamcmd && \
    curl -fsSL https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz | tar xz -C /tmp/steamcmd && \
    /tmp/steamcmd/steamcmd.sh +quit 2>/dev/null; true && \
    find / -name "steamclient.so" -path "*/linux32/*" 2>/dev/null -exec cp {} /usr/lib/steam/sdk32/ \; && \
    find / -name "steamclient.so" -path "*/linux64/*" 2>/dev/null -exec cp {} /usr/lib/steam/sdk64/ \; && \
    rm -rf /tmp/steamcmd /root/.local/share/Steam /root/.steam && \
    ldconfig

# JDK path convention: /opt/java/<major>/bin/java — scripts select by MC-required version.
RUN mkdir -p /opt/java && \
    ln -s /usr/lib/jvm/java-8-openjdk-amd64  /opt/java/8 && \
    ln -s /usr/lib/jvm/java-17-openjdk-amd64 /opt/java/17 && \
    ln -s /usr/lib/jvm/java-21-openjdk-amd64 /opt/java/21 && \
    ln -s /usr/lib/jvm/java-25-openjdk-amd64 /opt/java/25

COPY run-gameserver /usr/local/bin/run-gameserver
RUN chmod +x /usr/local/bin/run-gameserver

RUN useradd -m -s /bin/bash gameserver && \
    mkdir -p /data /scripts /defaults /depot

WORKDIR /data
