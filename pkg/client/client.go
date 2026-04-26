// Package client is the public Go SDK for talking to a Hearth coordinator
// over mTLS gRPC. Use it to submit jobs, watch their state, fetch results,
// upload/download blobs, and list workers — programmatically, from your
// own service or tool.
//
// The runtime side of Hearth (running handlers) lives in pkg/runner;
// most users will use one or the other. Mix both inside a single binary
// if you have a service that both produces and consumes work.
//
// Connecting requires a `.hearth` enrollment bundle (issued by
// `hearth enroll <name>` on the coordinator host):
//
//	c, err := client.Connect(ctx, "/path/to/admin.hearth")
//	if err != nil { return err }
//	defer c.Close()
//
//	id, err := c.Submit(ctx, job.Spec{Kind: "img2pdf", ...})
package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	grpcadapter "github.com/notpop/hearth/internal/adapter/transport/grpc"
	"github.com/notpop/hearth/internal/app"
	"github.com/notpop/hearth/internal/security/bundle"
	"github.com/notpop/hearth/internal/security/pki"
	"github.com/notpop/hearth/pkg/job"
)

// Sentinel errors. Use errors.Is to match.
var (
	// ErrNotFound is returned when the requested job, blob, or worker
	// id does not exist.
	ErrNotFound = errors.New("hearth: not found")

	// ErrInvalidTransition is returned when an operation is not valid
	// for the job's current state — e.g. cancelling a job that's
	// already terminal, or completing a lease the worker no longer
	// holds.
	ErrInvalidTransition = errors.New("hearth: invalid state transition")

	// ErrUnauthenticated is returned when the coordinator rejected
	// the bundle's certificate — wrong CA, expired cert, or revoked.
	ErrUnauthenticated = errors.New("hearth: certificate not accepted")

	// ErrInvalidArgument is returned when the request itself is malformed
	// (e.g. empty Kind, invalid character in Kind). The original message
	// from the coordinator is wrapped for context.
	ErrInvalidArgument = errors.New("hearth: invalid argument")
)

// Client is a connection to a Hearth coordinator.
type Client struct {
	inner *grpcadapter.Client
}

// Connect dials the coordinator using the given bundle for mTLS.
// Pass options to override addresses, etc.
func Connect(ctx context.Context, bundlePath string, opts ...Option) (*Client, error) {
	cfg := &config{}
	for _, o := range opts {
		o(cfg)
	}

	b, err := bundle.ReadFile(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("read bundle: %w", err)
	}

	target := cfg.addr
	if target == "" {
		if len(b.Manifest.CoordinatorAddrs) == 0 {
			return nil, fmt.Errorf("client: bundle has no coordinator addresses; pass client.WithAddr(...)")
		}
		target = b.Manifest.CoordinatorAddrs[0]
	}
	host := strings.SplitN(target, ":", 2)[0]

	tlsCfg, err := pki.ClientTLSConfig(b.CACertPEM, b.ClientCertPEM, b.ClientKeyPEM, host)
	if err != nil {
		return nil, err
	}

	inner, err := grpcadapter.Dial(ctx, target, tlsCfg)
	if err != nil {
		return nil, mapErr(err)
	}
	return &Client{inner: inner}, nil
}

// Close releases the connection.
func (c *Client) Close() error { return c.inner.Close() }

// --- Job lifecycle ------------------------------------------------------

// Submit enqueues a new job and returns its id.
func (c *Client) Submit(ctx context.Context, spec job.Spec) (job.ID, error) {
	id, err := c.inner.SubmitJob(ctx, spec)
	return id, mapErr(err)
}

// Get returns the current state of a single job.
func (c *Client) Get(ctx context.Context, id job.ID) (job.Job, error) {
	j, err := c.inner.GetJob(ctx, id)
	return j, mapErr(err)
}

// List returns jobs matching filter, newest first.
func (c *Client) List(ctx context.Context, filter ListFilter) ([]job.Job, error) {
	jobs, err := c.inner.ListJobs(ctx, app.ListFilter{
		States: filter.States,
		Kinds:  filter.Kinds,
		Limit:  filter.Limit,
	})
	return jobs, mapErr(err)
}

// ListFilter narrows a List query.
type ListFilter struct {
	States []job.State
	Kinds  []string
	Limit  int
}

// Cancel transitions a job to Cancelled. Calling Cancel on a terminal
// job returns ErrInvalidTransition.
func (c *Client) Cancel(ctx context.Context, id job.ID) error {
	return mapErr(c.inner.CancelJob(ctx, id))
}

// Watch streams job updates to fn until the job reaches a terminal
// state, the stream closes, or ctx is cancelled. The first call delivers
// the current snapshot.
func (c *Client) Watch(ctx context.Context, id job.ID, fn func(job.Job)) error {
	return mapErr(c.inner.WatchJob(ctx, id, fn))
}

// --- Blob I/O -----------------------------------------------------------

// PutBlob streams r into the coordinator's content-addressed store and
// returns its BlobRef. Identical content yields the same ref.
func (c *Client) PutBlob(ctx context.Context, r io.Reader) (job.BlobRef, error) {
	ref, err := c.inner.PutBlob(ctx, r)
	return ref, mapErr(err)
}

// GetBlob opens a stream for a blob. Caller closes.
func (c *Client) GetBlob(ctx context.Context, ref job.BlobRef) (io.ReadCloser, error) {
	rc, err := c.inner.GetBlob(ctx, ref)
	return rc, mapErr(err)
}

// --- Cluster info -------------------------------------------------------

// Nodes returns the currently-registered workers.
func (c *Client) Nodes(ctx context.Context) ([]NodeInfo, error) {
	infos, err := c.inner.ListNodes(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]NodeInfo, len(infos))
	for i, n := range infos {
		out[i] = NodeInfo{
			ID: n.ID, Hostname: n.Hostname, OS: n.OS, Arch: n.Arch,
			Kinds: n.Kinds, Version: n.Version,
			JoinedAt: n.JoinedAt, LastSeen: n.LastSeen,
		}
	}
	return out, nil
}

// NodeInfo describes a registered worker.
type NodeInfo struct {
	ID       string
	Hostname string
	OS       string
	Arch     string
	Kinds    []string
	Version  string
	JoinedAt time.Time
	LastSeen time.Time
}

// --- Options ------------------------------------------------------------

type config struct {
	addr string
}

// Option configures a Connect call.
type Option func(*config)

// WithAddr overrides the coordinator address from the bundle.
func WithAddr(addr string) Option {
	return func(c *config) { c.addr = addr }
}

// --- Error mapping ------------------------------------------------------

// mapErr maps gRPC status codes to the package's sentinel errors.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	switch st.Code() {
	case codes.OK:
		return nil
	case codes.NotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, st.Message())
	case codes.FailedPrecondition:
		return fmt.Errorf("%w: %s", ErrInvalidTransition, st.Message())
	case codes.InvalidArgument:
		return fmt.Errorf("%w: %s", ErrInvalidArgument, st.Message())
	case codes.Unauthenticated, codes.PermissionDenied:
		return fmt.Errorf("%w: %s", ErrUnauthenticated, st.Message())
	default:
		return err
	}
}
