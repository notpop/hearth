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

Prerequisites: Go ≥ 1.26 (or use the bundled `nix develop` shell — see *Development* below). Or grab a prebuilt binary from the [latest release](https://github.com/notpop/hearth/releases).

### On the always-on host (one command)

```bash
hearth coordinator
```

First run auto-creates the CA, server cert, and a local admin bundle, then starts the gRPC server. Subsequent runs reuse them.

### Submit a job from the same host

```bash
hearth submit --kind echo --payload "hi"
hearth status
```

The CLI auto-discovers `./.hearth/admin.hearth` (or `~/.hearth/admin.hearth`) so no `--bundle` is needed when running on the coordinator host.

### Add a worker from another machine

On the coordinator host, issue a bundle:

```bash
hearth enroll --addr <coord-ip>:7843 my-laptop    # → my-laptop.hearth (~1 KB)
```

Move `my-laptop.hearth` to the worker host (USB, scp, SD card — your choice).

That host can now connect to the coordinator (`hearth worker --bundle my-laptop.hearth`) but the OSS binary registers no handlers — to actually process jobs, build a worker binary that imports `pkg/runner` (see below).

## Building your own worker

The OSS API surface external projects depend on is exactly two packages:

- `github.com/notpop/hearth/pkg/worker` — the `Handler` interface
- `github.com/notpop/hearth/pkg/runner` — `RunWorker` (or `Run` for more control)

A complete worker is ~15 lines:

```go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "syscall"

    "github.com/notpop/hearth/pkg/runner"
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
    if len(os.Args) != 2 {
        log.Fatal("usage: my-worker <bundle.hearth>")
    }
    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer cancel()

    if err := runner.RunWorker(ctx, os.Args[1], myHandler{}); err != nil {
        log.Fatal(err)
    }
}
```

`runner.RunWorker` reads the bundle, dials the coordinator over mTLS, registers
the worker, and runs the handler loop until the context is cancelled. For more
control (custom logger, multiple handlers, address override), use `runner.Run`
with `runner.Options`.

The polished example with a real handler lives at `examples/img2pdf/cmd/img2pdf-worker/main.go`.

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

## CLI reference

```
hearth coordinator [--listen ...] [--data ...] [--ca ...] [--mdns]
hearth enroll [--addr <host:port>] [--out <path>] [--validity <dur>] <name>
hearth submit  [--bundle <path>] [--coordinator <addr>] --kind <k> [--payload <s>] [--blob <path> ...]
hearth status  [--bundle <path>] [--coordinator <addr>] [--job <id>] [--watch] [--limit N]
hearth cancel  [--bundle <path>] [--coordinator <addr>] <job-id>
hearth nodes   [--bundle <path>] [--coordinator <addr>]
hearth ca      init [--dir <path>] [--name <cn>]
hearth worker  --bundle <path>     # OSS binary, no handlers — for connectivity tests
hearth version
```

## Roadmap

Done in 0.1.x:

- [x] mTLS-by-default with self-rooted CA
- [x] Single-file worker enrollment (`.hearth` bundle)
- [x] mDNS auto-discovery (`_hearth._tcp.local`)
- [x] SQLite WAL durable queue + filesystem CAS blob store
- [x] Coordinator auto-bootstrap (zero-config first run)
- [x] CLI auto-discovers admin bundle (no `--bundle` on coord host)
- [x] CLI `--blob` for input file uploads
- [x] CancelJob end-to-end (`hearth cancel <id>`)
- [x] WatchJob push notifications (in-memory pub/sub)
- [x] Worker auto-reconnect with exponential backoff

Not on the roadmap:

- Multi-coordinator HA — home-scale doesn't need it; SQLite WAL means a coordinator restart loses no committed work, and only one host is "always on" anyway. SPOF is acceptable.

## License

[MIT](./LICENSE) © 2026 notpop
