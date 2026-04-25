# Hearth

> Distributed task runner for your home network. Single binary, mTLS by default, zero-config worker enrollment.

[日本語版 README](./README.ja.md)

Hearth lets you spread heavy background work — image-to-PDF, video transcoding, batch ML inference, anything CPU-bound — across the computers you already have at home. One host queues the jobs; the others pick them up and do the work; the results come back. Coordinator and worker can run on any OS — Mac, Windows, Linux, NAS — in any combination. Nothing leaves your LAN.

## Status

**Alpha.** The wire protocol, public API surface, and bundle format may still change. Pin a commit if you depend on it.

## Why

A typical home has 3–5 computers (Mac, Windows, Linux, NAS, …) and a single one of them gets bogged down whenever a heavy batch shows up. Cloud queue runners (SQS, Celery + Redis, Temporal) solve the same problem at the cost of either leaving the LAN or operating real infrastructure. Hearth is the smallest thing that:

- runs entirely **inside your LAN** (mDNS discovery, no external dependencies)
- ships as **one Go binary** with no cgo (cross-compiles to every OS your machines run)
- secures every connection with **mTLS** using a self-rooted CA
- enrolls a new worker with **one file** moved over USB / SD / scp
- tolerates workers joining and leaving mid-flight (lease + automatic reclaim)

## Concepts

| Term | What it is |
|---|---|
| **Coordinator** | The single host that owns the queue, schedules leases, stores blobs, and serves the gRPC API. Runs on whichever machine is "always on". |
| **Worker** | Any host that pulls jobs of one or more *kinds* and runs your `Handler`. |
| **Handler** | A user-implemented Go interface (`pkg/worker.Handler`) that does the actual work. The OSS Hearth binary doesn't ship handlers; you build a worker binary that imports Hearth and registers your handlers. |
| **Job kind** | A routing string. Workers advertise the kinds they accept; the coordinator only hands them matching jobs. |
| **Bundle** | A single `.hearth` file (tar.gz, ~1 KB) containing the CA cert, a per-worker certificate + key, and the coordinator address(es). The only thing a new machine needs to join. |
| **Lease** | A worker's temporary claim on a job, renewed by heartbeats. If the worker disappears, the lease expires and the job is automatically re-queued. |

## Quick start

Prerequisites: Go ≥ 1.26 (or use the bundled `nix develop` shell — see *Development* below).

### 1. Build the binary

```bash
go build ./cmd/hearth
```

You now have `./hearth` — copy it to any host that will run a coordinator, worker, or CLI.

### 2. Initialise the CA (once, on the coordinator host)

```bash
./hearth ca init
# Hearth CA initialised at /home/you/.hearth/ca
#   CN     = hearth-home-ca
#   expires = 2036-...
```

### 3. Issue an enrollment bundle for each worker (and for yourself, if you'll use the CLI)

```bash
./hearth enroll --addr coordinator.local:7843 imac-2
# Wrote enrollment bundle: imac-2.hearth
```

Move `imac-2.hearth` to that machine via USB, SD card, or whatever you trust.

### 4. Run the coordinator

```bash
./hearth coordinator --listen 0.0.0.0:7843 --data ./.hearth
# hearth coordinator listening on 0.0.0.0:7843
# mdns advertising as imac._hearth._tcp.local.
```

### 5. Submit a job

```bash
./hearth submit --bundle ./admin.hearth --kind echo --payload "hi"
# 4b29b6cb0adaea7ce347367a5575486f
./hearth status --bundle ./admin.hearth
```

### 6. Run a worker

The OSS `hearth worker --bundle ...` command verifies connectivity but registers no handlers. To actually do work, you build your own worker binary — see *Building your own worker* below or the runnable example at `examples/img2pdf/cmd/img2pdf-worker`.

## Building your own worker

A worker binary is ~30 lines plus your handler. Minimal recipe:

```go
package main

import (
    "context"
    "os"
    "runtime"
    "strings"
    "time"

    grpcadapter "github.com/notpop/hearth/internal/adapter/transport/grpc"
    "github.com/notpop/hearth/internal/app"
    "github.com/notpop/hearth/internal/app/workerrt"
    "github.com/notpop/hearth/internal/security/bundle"
    "github.com/notpop/hearth/internal/security/pki"
    "github.com/notpop/hearth/pkg/worker"
)

type myHandler struct{}

func (myHandler) Kind() string { return "my-task" }

func (myHandler) Handle(ctx context.Context, in worker.Input) (worker.Output, error) {
    // 1. Read inputs (in.Payload, in.Blobs[i].Open()).
    // 2. Do the work; honour ctx.Done().
    // 3. Return worker.Output{Payload, Blobs}.
    return worker.Output{Payload: []byte("done")}, nil
}

func main() {
    b, _ := bundle.ReadFile(os.Args[1])
    addr := b.Manifest.CoordinatorAddrs[0]
    host := strings.SplitN(addr, ":", 2)[0]

    tlsCfg, _ := pki.ClientTLSConfig(b.CACertPEM, b.ClientCertPEM, b.ClientKeyPEM, host)
    client, _ := grpcadapter.Dial(context.Background(), addr, tlsCfg)
    defer client.Close()

    _, _ = client.RegisterWorker(context.Background(), app.WorkerInfo{
        ID: b.Manifest.WorkerID, Hostname: hostname(), OS: runtime.GOOS, Arch: runtime.GOARCH,
        Kinds: []string{"my-task"}, LastSeen: time.Now().UTC(),
    })

    rt := workerrt.New(workerrt.Options{
        WorkerID: b.Manifest.WorkerID,
        Handlers: []worker.Handler{myHandler{}},
        Client:   client,
    })
    _ = rt.Run(context.Background())
}

func hostname() string { h, _ := os.Hostname(); return h }
```

The full, polished version lives at `examples/img2pdf/cmd/img2pdf-worker/main.go`.

### Handler contract

- **Idempotent.** Hearth may re-deliver a job after a crash or lease expiry. Your handler must accept that "the same job runs twice" results in the same outcome (or a deduplicated one).
- **Honour `ctx`.** When the lease is lost or the coordinator asks the worker to abandon, `ctx.Done()` fires. Stop work and return.
- **Don't hide failures.** Return an error to trigger Hearth's backoff + retry. Errors are recorded on the job; transient failures will be retried until `MaxAttempts` is reached.
- **Keep payloads small.** Anything bigger than a few KB belongs in a blob (`worker.OutputBlob{Reader: ...}`) — the runtime CAS-stores it and surfaces just the SHA-256.

## Architecture

Hearth is a four-layer codebase, with each layer testable on its own:

```
┌─────────────────────────────────────────────────────────────┐
│ pkg/job, pkg/worker        public API (stable)              │
├─────────────────────────────────────────────────────────────┤
│ internal/domain/{jobsm,retry}    pure functions, no I/O     │  ← 100% testable
├─────────────────────────────────────────────────────────────┤
│ internal/app                     orchestration + ports      │
│   coordinator/   workerrt/                                  │
├─────────────────────────────────────────────────────────────┤
│ internal/adapter                 I/O implementations        │
│   store/{memstore,sqlite}    blob/fs    registry/memregistry│
│   discovery/mdns             transport/grpc                 │
│   security/{pki,bundle}                                     │
└─────────────────────────────────────────────────────────────┘
                                   ↑
                                   │ wired together at
                              cmd/hearth/
```

The discipline that keeps this from collapsing:

- `internal/domain/*` imports nothing from `internal/adapter/*` — and may not import `time.Now`, `context`, or any I/O. Time and randomness are passed in by value.
- `internal/app/*` depends only on the domain and on the `app.Store` / `app.BlobStore` / `app.Clock` / `app.WorkerRegistry` interfaces declared in `internal/app/ports.go`.
- Concrete adapters live in `internal/adapter/*` and are wired into `internal/app` only at the cmd layer.

This makes the SQLite store interchangeable with `memstore` (the in-memory test fake) without changing a single line of orchestration code.

### Storage

- **Jobs**: SQLite in WAL mode (`modernc.org/sqlite`, no cgo). Single `*sql.DB` with `MaxOpenConns=1` serialises writes and avoids `SQLITE_BUSY` retries. Pragmas: `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000`. For LAN-scale workloads this is plenty; the `app.Store` interface lets you drop in BadgerDB or any other engine without touching orchestration.
- **Blobs**: filesystem CAS at `<data>/blobs/<sha[:2]>/<sha>`. Atomic on rename, deduplicated by content. MB-class payloads should always go through here, not through the SQLite payload column.

### Security

- **mTLS everywhere.** Clients (workers, CLI) and the server present certificates signed by the home CA. TLS 1.3 only, Ed25519 keys.
- **CA-rooted trust.** A single self-signed CA cert is generated at `hearth ca init` and never leaves the coordinator. Worker certs are issued from it on-demand.
- **One-file enrollment.** `hearth enroll <name>` produces `<name>.hearth` (a tar.gz containing CA cert, client cert, client key, coordinator addresses). Move it via trusted physical medium.

### Discovery

mDNS service `_hearth._tcp.local`. The coordinator advertises its host + port; workers fall back to discovery when their bundle doesn't include an explicit address. No router config, no DNS server.

### Failure handling

- **Worker crash mid-job**: heartbeat stops → lease TTL elapses → coordinator's reclaim sweeper requeues the job → another worker picks it up. The crashed worker's attempt counts toward `MaxAttempts`, with the configured backoff applied to the next try.
- **Coordinator crash**: SQLite WAL ensures no committed job is lost. On restart, expired leases are reclaimed on the first sweep.
- **Network partition**: workers pause naturally (long-poll `Lease` errors out, retry with backoff). When the network heals, leases that didn't survive the partition are reclaimed; any in-flight job whose worker reconnected resumes its heartbeats.

## Development

### Reproducible environment

The repo ships a Nix flake. With Nix installed:

```bash
nix develop          # drops you into a shell with Go 1.26.2, gopls, golangci-lint, sqlite, protoc, etc.
go test ./...
```

Or `direnv allow` if you have direnv installed.

### Common tasks

```bash
go test ./...               # unit + integration tests (full suite ~10s)
go test -race ./...         # data race detector
go test -cover ./...        # coverage
go vet ./...
go build ./cmd/hearth       # the all-in-one CLI binary
go build ./examples/img2pdf/cmd/img2pdf-worker

# Regenerate gRPC stubs after editing the .proto:
protoc --proto_path=api/proto \
  --go_out=. --go_opt=module=github.com/notpop/hearth \
  --go-grpc_out=. --go-grpc_opt=module=github.com/notpop/hearth \
  api/proto/hearth/v1/hearth.proto
```

### Layout

See *Architecture* above. In one paragraph: `pkg/` is the public surface; `internal/domain/` is pure logic; `internal/app/` is orchestration; `internal/adapter/` is I/O; `cmd/` is glue; `examples/` shows users how to plug in handlers.

## Roadmap (not yet implemented)

- [ ] `CancelJob` end-to-end (server side returns Unimplemented today)
- [ ] `WatchJob` push notifications (currently a server-side poll)
- [ ] CLI `submit` with blob inputs (`--blob <path>`)
- [ ] Worker auto-reconnect with exponential backoff
- [ ] Multi-coordinator HA (single coordinator today; LAN scale rarely needs it)

## License

[MIT](./LICENSE) © 2026 notpop
