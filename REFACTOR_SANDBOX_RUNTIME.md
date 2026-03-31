# Unified Sandbox Runtime

Promote sandbox as the default runtime. Docker remains supported but is no longer the default.

## Why

- Docker is a heavyweight daemon dependency that most users don't need
- Current process runtime is half-broken (ports, copy ops, resource limits)
- Maintaining two runtimes with different behaviors doubles the bug surface
- bwrap + systemd gives us everything Docker provides for game servers, without the daemon
- Single default runtime means one code path to test, debug, and document

## Architecture

### Sandbox runtime (default)
- **bwrap** — Filesystem isolation (`--unshare-pid`, mount namespace). Bundled as static binary (~50KB).
- **slirp4netns** — Network namespace isolation for all gameservers. Bundled as static binary (~2MB).
- **systemd transient units** — Process lifecycle, cgroups v2 (memory/CPU/IO limits), crash recovery, process survival across restarts. `SocketBindAllow` for automatic port restriction.
- **OCI images** — `go-containerregistry` (already in binary, zero size increase). Parallel layer downloads, layer caching by digest, overlayfs mounting. No extraction step when overlayfs is available — layers used directly as filesystem overlays. Falls back to flat extraction on older kernels.

### OCI image strategy
- **Pull**: parallel layer downloads via `go-containerregistry`. Cache compressed layer blobs by digest in `{dataDir}/layers/{digest}`.
- **Mount (fast path)**: overlayfs — stack cached layers as read-only lowers, gameserver volume as upper. Zero extraction, instant for repeat use. Shared across all gameservers using the same image.
- **Extract (fallback)**: if overlayfs unavailable (kernel <5.11, no permissions), extract layers to flat directory per image. Still cached by image digest — only extracts once per unique image. Log a warning on startup recommending kernel 5.11+ for optimal performance.
- **Steam depot**: same overlayfs approach. Depot is the read-only lower, gameserver volume is the upper. 50GB ARK depot shared across all ARK servers.
- **No new dependencies**: `go-containerregistry` already in binary, overlayfs is a kernel feature (`syscall.Mount`). No skopeo, no umoci, no binary size increase.
- **Kill box64**: ARM emulation is out of scope. Can revisit later if needed.

### Docker runtime (optional)
- Existing Docker implementation, kept working behind the Worker interface.
- For users who already have Docker and prefer it, or need Docker-specific features.
- No Docker-specific concepts leak outside `worker/docker/`.

### System requirements
- **systemd** — Required for sandbox runtime. Every mainstream Linux distro has it. Users without systemd (Alpine, containers) use Docker runtime.
- **Linux** — Both runtimes are Linux-only. Windows support via WSL2 (has systemd since 2022).

## Decisions

- **Network isolation: always-on** — Every gameserver gets its own network namespace via slirp4netns. No opt-in/opt-out, no extra code paths. Homelab and business get the same isolation.
- **SocketBindAllow: always-on** — We know the allocated ports, so we restrict automatically. Free security, zero cost.
- **systemd required for sandbox** — No degraded mode. If systemd isn't available, use Docker runtime. Clear error at startup if sandbox is selected without systemd.

## Directory restructure

### Current (messy)
```
docker/              -- Docker client (why at root?)
worker/
  worker.go          -- Worker interface
  types.go           -- ContainerInfo, ContainerStats, etc.
  fileops.go         -- shared file operations
  local/             -- Docker Worker (misleading name)
  process/           -- Process Worker (half-broken)
  remote/            -- gRPC remote Worker
  agent/             -- gRPC server wrapping local Worker
  pb/                -- generated protobuf
  logparse/          -- log parsing utils
```

### Target (clean)
```
worker/
  worker.go          -- Worker interface
  types.go           -- InstanceInfo, InstanceStats, etc.
  fileops.go         -- shared file operations
  sandbox/           -- Sandbox runtime (bwrap + slirp + systemd)
    sandbox.go       -- implements Worker interface
    bwrap.go         -- bwrap command builder
    network.go       -- slirp4netns setup/teardown
    lifecycle.go     -- start/stop/exec
    stats.go         -- cgroup v2 stat reading
    image.go         -- OCI pull, layer cache, overlayfs mount + fallback extract
    runtime/         -- internal runtime backend interface
      systemd.go     -- systemd transient unit management
  docker/            -- Docker runtime (moved from root docker/ + worker/local/)
    docker.go        -- Docker client + Worker implementation
  remote/            -- gRPC remote Worker (unchanged)
  agent/             -- gRPC server (unchanged)
  pb/                -- generated protobuf (unchanged)
  logparse/          -- log parsing (unchanged)
```

Each runtime is self-contained under `worker/`. Only `cli/serve_worker.go` picks which one to instantiate. The rest of the codebase only sees the Worker interface.

### Internal runtime abstraction
```
worker/sandbox/runtime/ is internal to the sandbox package:
  - runtime.go       -- interface: StartUnit, StopUnit, SetLimits, ReadStats
  - systemd.go       -- systemd implementation
  // future: cgroups.go (direct cgroups v2 without systemd)
  // future: launchd.go (macOS)
```

Swapping the runtime backend is a single-package change. Nothing outside `worker/sandbox/` changes.

## Rename plan

### Container → Instance (mechanical, no behavior change)
- `ContainerID` → `InstanceID` (~117 occurrences across ~26 files)
- `ContainerInfo` → `InstanceInfo`
- `ContainerStats` → `InstanceStats`
- `ContainerEvent` → `InstanceEvent`
- `ContainerOptions` → `InstanceOptions`
- `container_id` in schema/JSON → `instance_id`
- `container_runtime` config → `runtime` (values: `sandbox`, `docker`)
- Worker interface method names: `CreateContainer` → `CreateInstance`, etc.
- Proto: update message/field names, regenerate

### Package moves
- `docker/` → `worker/docker/` (merge with `worker/local/`)
- `worker/process/` → `worker/sandbox/`
- `worker/local/` → deleted (absorbed into `worker/docker/`)

## Migration phases

### Phase 1: Rename + restructure
- Rename all Container→Instance references
- Move packages to new layout
- Update imports
- Mechanical, no behavior change, both runtimes still work

### Phase 2: Build sandbox runtime
- Rewrite `worker/sandbox/` with proper systemd + bwrap + slirp4netns
- Fix all current process runtime gaps (ports, copy ops, resource limits, events)
- Bundle bwrap + slirp4netns (download on first use)
- Port binding: game binds inside network namespace, slirp forwards from host port
- Stats: cgroups v2 for memory/CPU, process accounting for network

### Phase 3: Feature parity audit
- All Worker interface methods work correctly
- All 50+ game definitions work
- Backup/restore, file ops, Steam depot, mods, console — all verified
- Full e2e test suite passes
- Install script defaults to sandbox

### Phase 4: Docker becomes opt-in
- Install script defaults to sandbox, offers Docker as opt-in
- Config `runtime: sandbox` (default) or `runtime: docker`
- Docs updated: sandbox is primary, Docker is alternative
- Docker runtime stays maintained but is not the focus

## Security model

All gameservers get the same isolation regardless of user archetype:

- **Filesystem**: bwrap restricted mounts — game sees only its rootfs + data directory
- **PID**: bwrap `--unshare-pid` — game only sees its own processes
- **Network**: bwrap `--unshare-net` + slirp4netns — isolated network namespace per gameserver
- **Resources**: systemd cgroups v2 — memory/CPU/IO hard limits
- **Ports**: systemd `SocketBindAllow` — can only bind allocated ports
- **Users**: `DynamicUser=yes` or per-gameserver UIDs
- **Syscalls**: Optional seccomp via bwrap `--seccomp`

## Secret isolation

- **Workers have no secrets.** Steam API keys, refresh tokens, admin tokens, and the database live on the controller only. Workers receive short-lived instructions via gRPC — they never store credentials.
- **Filesystem permissions.** gamejanitor's data directory (`/var/lib/gamejanitor/`) is owned by the service user, not readable by gameserver UIDs. bwrap never mounts the data directory into the sandbox — game servers can only see their own volume.
- **Sandbox escape impact.** If a game server escapes the sandbox on a worker node, it can access other game server volumes on that worker (bad, but limited blast radius). It cannot access the controller database, API tokens, or Steam credentials.

## Windows/Mac support path

- **Windows**: WSL2 has systemd. Sandbox stack works unmodified. Windows app = launcher that sets up WSL2 + opens web UI.
- **macOS**: No bwrap/systemd. Docker runtime is the path. Or future launchd backend in `worker/sandbox/runtime/`.

## Bundled binaries

Downloaded on first use, cached in data directory:
- bwrap: ~50KB static binary (amd64 + arm64)
- slirp4netns: ~2MB static binary (amd64 + arm64)
