# Hearth

> Distributed task runner for your home network. Single binary, mTLS by default, single-file worker enrollment.

[日本語版 README](./README.ja.md)

Hearth lets one host queue heavy background jobs (image-to-PDF, video transcoding, batch ML inference, …) and have other home machines pick them up over the LAN. Coordinator and worker can run on any OS — Mac, Windows, Linux, NAS — in any combination. Nothing leaves your network.

**Status: alpha.** Wire protocol, public API, and bundle format may still change. Pin a commit if you depend on it.

## Concepts

| Term | What it is |
|---|---|
| **Coordinator** | Single host that owns the queue, stores blobs, and serves the gRPC API. |
| **Worker** | Any host that pulls jobs of one or more *kinds* and runs your `Handler`. |
| **Handler** | A user-implemented Go interface (`pkg/worker.Handler`) that does the work. |
| **Bundle** | A single `.hearth` file (~1 KB) carrying CA cert, client cert + key, coordinator address. |

## Quick start

Grab a binary from the [latest release](https://github.com/notpop/hearth/releases) or `go build ./cmd/hearth`. Then:

```sh
# On the always-on host (auto-creates CA + admin bundle on first run):
hearth coordinator

# Submit a job from the same host (CLI auto-finds the admin bundle):
hearth submit --kind echo --payload "hi"
hearth status
```

### Adding a worker on another host

```sh
# On the coordinator:
hearth enroll --addr <coord-ip>:7843 my-laptop      # → my-laptop.hearth (~1 KB)

# Move my-laptop.hearth to the worker (USB, scp, SD card — your choice).
# On the worker, with your own handler-bearing binary (see below):
my-worker my-laptop.hearth
```

The OSS `hearth worker` command can also load a bundle for connectivity testing, but it ships with no handlers — to actually process jobs you build your own binary that imports `pkg/runner`.

## Building your own worker

External projects depend on exactly two packages:

- `github.com/notpop/hearth/pkg/worker` — the `Handler` interface
- `github.com/notpop/hearth/pkg/runner` — `RunWorker` (or `Run` for more control)

A complete worker is ~15 lines:

```go
package main

import (
    "context"; "log"; "os"; "os/signal"; "syscall"

    "github.com/notpop/hearth/pkg/runner"
    "github.com/notpop/hearth/pkg/worker"
)

type myHandler struct{}

func (myHandler) Kind() string { return "my-task" }

func (myHandler) Handle(ctx context.Context, in worker.Input) (worker.Output, error) {
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

`runner.RunWorker` reads the bundle, dials the coordinator over mTLS, registers the worker, and runs the handler loop until the context is cancelled. The polished example with a real handler lives at `examples/img2pdf/cmd/img2pdf-worker/main.go`.

### Handler contract

- **Idempotent.** Hearth may re-deliver a job after a crash or lease expiry.
- **Honour `ctx`.** Cancellation fires when the lease is lost or the coordinator asks the worker to abandon. Stop work and return.
- **Return errors to retry.** Hearth applies the job's backoff policy until `MaxAttempts`.
- **Use blobs for big payloads.** `worker.OutputBlob{Reader: ...}` — the runtime CAS-stores it and surfaces just the SHA-256.

## CLI reference

```
hearth coordinator [--listen ...] [--data ...] [--ca ...] [--mdns]
hearth enroll      [--addr <host:port>] [--out <path>] [--validity <dur>] <name>
hearth submit      [--bundle <path>] --kind <k> [--payload <s>] [--blob <path> ...]
hearth status      [--bundle <path>] [--job <id>] [--watch] [--limit N]
hearth cancel      [--bundle <path>] <job-id>
hearth nodes       [--bundle <path>]
hearth ca init     [--dir <path>] [--name <cn>]
hearth worker      --bundle <path>     # connectivity check only — no handlers
hearth version
```

The CLI commands that take `--bundle` fall back, in order, to `$HEARTH_BUNDLE`, `./.hearth/admin.hearth`, and `~/.hearth/admin.hearth`, so you can omit the flag when running on the coordinator host.

## Network notes

Hearth lives entirely on your LAN. mTLS-authenticated gRPC is the only wire protocol; mDNS is used for zero-config discovery (`_hearth._tcp.local`).

A typical home router bridges WiFi and Ethernet at L2, so a Mac on WiFi and a PC on a wired LAN talk to the coordinator without any extra setup. mDNS multicast usually reaches both interfaces too. If your router enables AP isolation or segregated guest networks, mDNS may not reach across — fall back to passing an explicit IP to `hearth enroll --addr <ip>:7843`, which embeds it in the bundle.

## Development

```sh
nix develop          # Go 1.26 + tooling pinned in flake.nix
go test ./...        # ~10 s
go build ./cmd/hearth
```

`pkg/` is the public API surface. Everything else lives under `internal/` and may change.

## License

[MIT](./LICENSE) © 2026 notpop
