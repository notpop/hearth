# Usage Guide

[日本語版](./USAGE.ja.md) · [Project README](../README.md)

This guide is the deep-end companion to the README. It covers every public
API surface, every realistic integration pattern, and the contracts your
code is expected to honour.

---

## Table of contents

1. [Public API surface](#public-api-surface)
2. [Use cases](#use-cases)
   - [A. Build your own worker](#a-build-your-own-worker)
   - [B. Submit jobs programmatically](#b-submit-jobs-programmatically)
   - [C. Worker + submitter in one binary](#c-worker--submitter-in-one-binary)
   - [D. CLI only (no Go code)](#d-cli-only-no-go-code)
3. [Handler contract](#handler-contract)
4. [Spec defaults](#spec-defaults)
5. [Error handling](#error-handling)
6. [Progress reporting](#progress-reporting)
7. [Blob I/O patterns](#blob-io-patterns)
8. [Troubleshooting](#troubleshooting)

---

## Public API surface

External Go projects depend on **only** these packages:

| Package | Purpose |
|---|---|
| `github.com/notpop/hearth/pkg/job` | Domain data types (Job, Spec, State, BlobRef, …). No behaviour, just shapes. |
| `github.com/notpop/hearth/pkg/worker` | The `Handler` interface + `Input` / `Output` types. Implement Handler in your code. |
| `github.com/notpop/hearth/pkg/runner` | Run a worker process: `RunWorker(ctx, bundlePath, handlers...)`. |
| `github.com/notpop/hearth/pkg/client` | Connect to a coordinator from your own service: submit, watch, blob I/O. |

Anything under `internal/` is private to this module and **cannot** be
imported from external code (Go enforces this). If you find yourself
wanting something that lives there, file an issue — the answer is either
"that's reasonable, let's promote it to pkg/" or "here's the recommended
public alternative."

---

## Use cases

### A. Build your own worker

You want home machines to pick up jobs and run handlers you wrote.

Minimal worker (~15 lines):

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

For more control, use `runner.Run(ctx, runner.Options{...})`:

```go
err := runner.Run(ctx, runner.Options{
    BundlePath:      "/path/to/worker.hearth",
    Handlers:        []runner.Handler{myHandler{}, otherHandler{}},
    AddrOverride:    "192.168.1.10:7843", // override bundle's CoordinatorAddrs
    Version:         "my-worker/1.0",
    LeaseTTL:        60 * time.Second,
    PollTimeout:     30 * time.Second,
    HeartbeatPeriod: 15 * time.Second,
    Logger:          slog.New(slog.NewJSONHandler(os.Stderr, nil)),
})
```

Multiple handlers in a single binary are supported — each `Kind()` value
must be unique.

### B. Submit jobs programmatically

You want a Go service (HTTP API, Slack bot, scheduler) to push jobs into
Hearth without shelling out to the CLI.

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/notpop/hearth/pkg/client"
    "github.com/notpop/hearth/pkg/job"
)

func main() {
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    c, err := client.Connect(ctx, "/path/to/admin.hearth")
    if err != nil { log.Fatal(err) }
    defer c.Close()

    id, err := c.Submit(ctx, job.Spec{
        Kind:        "img2pdf",
        Payload:     []byte(`{"output":"album.pdf"}`),
        MaxAttempts: 3,
        LeaseTTL:    5 * time.Minute,
    })
    if err != nil { log.Fatal(err) }
    log.Printf("submitted: %s", id)

    // Watch until done, terminal state ends the stream.
    err = c.Watch(ctx, id, func(j job.Job) {
        log.Printf("[%s] %s", j.UpdatedAt.Format("15:04:05"), j.State)
        if j.Progress != nil {
            log.Printf("  progress: %.0f%% — %s", j.Progress.Percent*100, j.Progress.Message)
        }
    })
    if err != nil { log.Fatal(err) }
}
```

Submitting jobs with file inputs uses blobs:

```go
f, _ := os.Open("photo.png")
defer f.Close()

ref, _ := c.PutBlob(ctx, f) // streams to coordinator's CAS

id, _ := c.Submit(ctx, job.Spec{
    Kind:  "img2pdf",
    Blobs: []job.BlobRef{ref},
})
```

### C. Worker + submitter in one binary

Some services both produce and consume Hearth jobs (e.g. a fan-out
service that splits a job into smaller ones). Just import both packages:

```go
import (
    "github.com/notpop/hearth/pkg/client"
    "github.com/notpop/hearth/pkg/runner"
    "github.com/notpop/hearth/pkg/worker"
)

func main() {
    // Submit side
    c, _ := client.Connect(ctx, "/admin.hearth")
    defer c.Close()
    go submitLoop(c)

    // Worker side
    _ = runner.RunWorker(ctx, "/worker.hearth", subHandler{c: c})
}
```

The handler can use the embedded client to fan out child jobs:

```go
func (h subHandler) Handle(ctx context.Context, in worker.Input) (worker.Output, error) {
    pages := splitWork(in.Payload)
    for _, p := range pages {
        h.c.Submit(ctx, job.Spec{Kind: "img2pdf-page", Payload: p})
    }
    return worker.Output{}, nil
}
```

### D. CLI only (no Go code)

Use the `hearth` binary directly. See the [CLI reference](../README.md#cli-reference)
in the README. Programmatic integrations are still possible via shell:

```sh
JOB_ID=$(hearth submit --kind img2pdf --blob photo.png)
hearth status --job "$JOB_ID" --watch
```

This works but is fragile (output parsing, error handling). Prefer
pkg/client for production integrations.

---

## Handler contract

These rules apply to every Handler:

- **Idempotent.** Hearth may re-deliver a job after a crash, lease expiry,
  or network partition. Two runs of the same job ID must produce the same
  outcome (or a deduplicated one).
- **Honour `ctx`.** When the lease is lost or the coordinator asks the
  worker to abandon (e.g. user cancellation), `ctx.Done()` fires. Stop
  work and return any error — Hearth treats abandoned attempts as
  failures, but they will not be retried because the job is already
  terminal.
- **Return errors to retry.** Hearth applies the configured backoff and
  retries up to `Spec.MaxAttempts`. Returning `nil` marks the attempt as
  successful even if `Output.Payload` is empty.
- **Don't block forever.** Long-running handlers should call
  `in.Report` periodically so the coordinator can show progress. Lease
  TTL renews automatically via heartbeat; you don't manage that.
- **One handler instance is reused.** Hearth calls Handle concurrently
  across goroutines. If your handler needs per-job state, keep it in
  local variables, not on the receiver.

---

## Spec defaults

`job.Spec` is designed so the minimal form `Spec{Kind: "k"}` works.
Each field has a useful zero value:

| Field | Zero value behaviour |
|---|---|
| `Kind` | required (must match `^[a-z0-9._-]+$`, ≤ 64 chars) |
| `Payload` | empty slice |
| `Blobs` | none |
| `MaxAttempts` | 0 → 3 attempts. Set 1 for "no retry". Negative for unbounded. |
| `LeaseTTL` | 0 → 30s |
| `Backoff` | zero value → `{1s, 60s, ×2, 0.1 jitter}`. Partial-zero gets per-field defaults. |

Override any field individually:

```go
job.Spec{
    Kind:        "video-encode",
    MaxAttempts: 1,                        // no retry
    LeaseTTL:    30 * time.Minute,         // long jobs
    Backoff:     job.BackoffPolicy{Multiplier: 1}, // constant backoff (Initial defaults to 1s, Max to 60s)
}
```

---

## Error handling

`pkg/client` exposes sentinel errors. Use `errors.Is` to match.

```go
import "github.com/notpop/hearth/pkg/client"

j, err := c.Get(ctx, id)
switch {
case errors.Is(err, client.ErrNotFound):
    // 404 in your HTTP handler
case errors.Is(err, client.ErrInvalidArgument):
    // 400 — bundle is fine, request was malformed
case errors.Is(err, client.ErrInvalidTransition):
    // job is already terminal; treat as no-op
case errors.Is(err, client.ErrUnauthenticated):
    // re-issue the bundle; cert was rejected
case err != nil:
    // unknown / transport error — retry with backoff
default:
    // success
}
```

The wrapped underlying error is preserved, so `err.Error()` still has
the coordinator's message (the sentinel just makes `Is` work).

In handlers, return a plain error to trigger the retry policy. Hearth
records `err.Error()` on `Job.LastError`. There are no special error
types you need to use.

---

## Progress reporting

Long-running handlers call `in.Report(percent, message)` to surface
progress to watchers. The latest report wins; calling Report in a tight
loop is safe and cheap.

```go
func (h myHandler) Handle(ctx context.Context, in worker.Input) (worker.Output, error) {
    pages := decodePages(in.Payload)
    for i, p := range pages {
        in.Report(float64(i)/float64(len(pages)), fmt.Sprintf("page %d/%d", i+1, len(pages)))
        if err := process(ctx, p); err != nil {
            return worker.Output{}, err
        }
    }
    in.Report(1.0, "done")
    return worker.Output{}, nil
}
```

Internals:
- Reports are stored in an atomic; they don't generate per-call wire traffic.
- The next heartbeat (typically every 10s) carries the latest report.
- Watch subscribers see the update via `Job.Progress`.
- `hearth status` and `hearth status --watch` display percent + message.

`in.Report` may be `nil` (e.g. when you build `Input` directly in unit
tests). Guard with `if in.Report != nil { ... }` if you want your handler
to be safely usable both ways.

---

## Blob I/O patterns

### Inputs

The runtime materialises input blobs lazily — `in.Blobs[i].Open()`
opens a stream from the coordinator. Always close it.

```go
for _, b := range in.Blobs {
    rc, err := b.Open()
    if err != nil { return worker.Output{}, err }
    defer rc.Close()
    // stream from rc
}
```

### Outputs

Wrap any `io.Reader` you can produce. The runtime computes SHA-256 as it
streams to the coordinator.

```go
out := worker.Output{
    Blobs: []worker.OutputBlob{{
        Reader: someReader, // can be a *bytes.Reader, *os.File, etc.
        Size:   sizeHintInBytes, // optional; used for progress UIs
    }},
}
```

Idempotency note: identical content yields the same `BlobRef`. If two
attempts produce byte-for-byte the same output, the second `PutBlob`
returns the existing ref without re-storing.

---

## Troubleshooting

### "rpc error: code = Unavailable, transport: authentication handshake failed"

The CA in the bundle doesn't match the CA the coordinator was started
with. Causes:

- The bundle was issued before `hearth ca init` was rerun (e.g. the CA
  directory was deleted)
- Wrong bundle for this coordinator (e.g. dev CA bundle against prod
  coordinator)

Fix: re-issue the bundle (`hearth enroll <name>`) against the current CA.

### `hearth submit` says "no coordinator address"

Either the bundle has no `CoordinatorAddrs` (re-issue with `--addr`) or
no admin bundle exists in the auto-discovery paths
(`./.hearth/admin.hearth`, `~/.hearth/admin.hearth`). Pass `--bundle`
explicitly or `--coordinator <ip>:7843`.

### Worker doesn't pick up jobs

Check:
1. Worker is connected: `hearth nodes` lists it.
2. Worker advertises the right kinds: `hearth nodes` shows `KINDS` column.
3. The job's `Kind` matches one of the worker's kinds exactly
   (case-sensitive, no whitespace).

If `Kind` validation rejected your job at submit time, you'll see
`ErrInvalidArgument` with the regex hint in the message.

### mDNS doesn't resolve across WiFi/Ethernet

Most home routers bridge them at L2 by default and mDNS Just Works. If
yours has AP isolation enabled, fall back to a static address in the
bundle:

```sh
hearth enroll --addr 192.168.1.10:7843 my-worker
```

mDNS publishing can also be disabled per coordinator:

```sh
hearth coordinator --mdns=false
```
