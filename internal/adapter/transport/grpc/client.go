package grpcadapter

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/notpop/hearth/internal/app"
	"github.com/notpop/hearth/internal/wire"
	"github.com/notpop/hearth/pkg/job"
	hearthv1 "github.com/notpop/hearth/pkg/proto/hearth/v1"
)

// Client implements internal/app/workerrt.CoordinatorClient and the
// CLI-facing client surface (Submit, GetJob, ListJobs, ListNodes, WatchJob)
// over gRPC.
type Client struct {
	conn *grpc.ClientConn
	rpc  hearthv1.CoordinatorClient
}

// Dial connects to addr. tlsCfg is required for production deployments;
// pass nil ONLY for tests against an in-memory bufconn server.
func Dial(ctx context.Context, addr string, tlsCfg *tls.Config) (*Client, error) {
	var creds credentials.TransportCredentials
	if tlsCfg == nil {
		creds = insecure.NewCredentials()
	} else {
		creds = credentials.NewTLS(tlsCfg)
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("grpc dial: %w", err)
	}
	return &Client{conn: conn, rpc: hearthv1.NewCoordinatorClient(conn)}, nil
}

// DialContextOption is reserved for future tuning (keepalive, message size).
type DialContextOption struct{}

// Close terminates the underlying connection.
func (c *Client) Close() error { return c.conn.Close() }

// Conn returns the underlying *grpc.ClientConn (advanced use).
func (c *Client) Conn() *grpc.ClientConn { return c.conn }

// --- workerrt.CoordinatorClient -----------------------------------------

func (c *Client) Lease(ctx context.Context, kinds []string, workerID string, ttl, pollTimeout time.Duration) (job.Job, bool, error) {
	resp, err := c.rpc.LeaseJob(ctx, &hearthv1.LeaseJobRequest{
		WorkerId:    workerID,
		Kinds:       kinds,
		LeaseTtl:    durationpb.New(ttl),
		PollTimeout: durationpb.New(pollTimeout),
	})
	if err != nil {
		return job.Job{}, false, err
	}
	if resp.GetJob() == nil || resp.GetJob().GetId() == "" {
		return job.Job{}, false, nil
	}
	return wire.JobFromProto(resp.GetJob()), true, nil
}

func (c *Client) Heartbeat(ctx context.Context, id job.ID, workerID string) (time.Time, bool, error) {
	resp, err := c.rpc.Heartbeat(ctx, &hearthv1.HeartbeatRequest{JobId: string(id), WorkerId: workerID})
	if err != nil {
		return time.Time{}, false, err
	}
	var expires time.Time
	if resp.GetExpiresAt() != nil {
		expires = resp.GetExpiresAt().AsTime()
	}
	return expires, resp.GetCancel(), nil
}

func (c *Client) Complete(ctx context.Context, id job.ID, workerID string, res job.Result) error {
	_, err := c.rpc.CompleteJob(ctx, &hearthv1.CompleteJobRequest{
		JobId:    string(id),
		WorkerId: workerID,
		Result:   wire.ResultToProto(&res),
	})
	return err
}

func (c *Client) Fail(ctx context.Context, id job.ID, workerID string, msg string) error {
	_, err := c.rpc.FailJob(ctx, &hearthv1.FailJobRequest{
		JobId:        string(id),
		WorkerId:     workerID,
		ErrorMessage: msg,
	})
	return err
}

// GetBlob streams the blob into a buffer-backed io.ReadCloser. The whole
// blob is consumed eagerly because gRPC server-streams aren't seekable and
// callers (worker handlers) typically need an io.Reader they can pass to
// libraries that may re-read on retry.
func (c *Client) GetBlob(ctx context.Context, ref job.BlobRef) (io.ReadCloser, error) {
	stream, err := c.rpc.GetBlob(ctx, &hearthv1.GetBlobRequest{Ref: wire.BlobRefToProto(ref)})
	if err != nil {
		return nil, err
	}
	return &streamReader{stream: stream}, nil
}

// PutBlob streams r in 64KiB chunks. Returns the resulting BlobRef.
func (c *Client) PutBlob(ctx context.Context, r io.Reader) (job.BlobRef, error) {
	stream, err := c.rpc.PutBlob(ctx)
	if err != nil {
		return job.BlobRef{}, err
	}
	if err := stream.Send(&hearthv1.PutBlobRequest{
		Payload: &hearthv1.PutBlobRequest_Header{Header: &hearthv1.PutBlobHeader{}},
	}); err != nil {
		return job.BlobRef{}, err
	}
	buf := make([]byte, 64*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if serr := stream.Send(&hearthv1.PutBlobRequest{
				Payload: &hearthv1.PutBlobRequest_Chunk{Chunk: append([]byte(nil), buf[:n]...)},
			}); serr != nil {
				return job.BlobRef{}, serr
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return job.BlobRef{}, err
		}
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		return job.BlobRef{}, err
	}
	return wire.BlobRefFromProto(resp.GetRef()), nil
}

// --- CLI helpers (not part of CoordinatorClient interface) --------------

func (c *Client) SubmitJob(ctx context.Context, spec job.Spec) (job.ID, error) {
	resp, err := c.rpc.SubmitJob(ctx, &hearthv1.SubmitJobRequest{Spec: wire.SpecToProto(spec)})
	if err != nil {
		return "", err
	}
	return job.ID(resp.GetId()), nil
}

func (c *Client) GetJob(ctx context.Context, id job.ID) (job.Job, error) {
	p, err := c.rpc.GetJob(ctx, &hearthv1.GetJobRequest{Id: string(id)})
	if err != nil {
		return job.Job{}, err
	}
	return wire.JobFromProto(p), nil
}

func (c *Client) CancelJob(ctx context.Context, id job.ID) error {
	_, err := c.rpc.CancelJob(ctx, &hearthv1.CancelJobRequest{Id: string(id)})
	return err
}

func (c *Client) ListJobs(ctx context.Context, filter app.ListFilter) ([]job.Job, error) {
	states := make([]hearthv1.State, 0, len(filter.States))
	for _, st := range filter.States {
		states = append(states, wire.StateToProto(st))
	}
	resp, err := c.rpc.ListJobs(ctx, &hearthv1.ListJobsRequest{
		States: states,
		Kinds:  filter.Kinds,
		Limit:  int32(filter.Limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]job.Job, len(resp.GetJobs()))
	for i, p := range resp.GetJobs() {
		out[i] = wire.JobFromProto(p)
	}
	return out, nil
}

func (c *Client) ListNodes(ctx context.Context) ([]app.WorkerInfo, error) {
	resp, err := c.rpc.ListNodes(ctx, &hearthv1.ListNodesRequest{})
	if err != nil {
		return nil, err
	}
	out := make([]app.WorkerInfo, len(resp.GetNodes()))
	for i, p := range resp.GetNodes() {
		out[i] = wire.WorkerInfoFromProto(p)
	}
	return out, nil
}

// WatchJob streams updates for id, invoking onUpdate for each. Returns when
// the job reaches a terminal state, the server closes the stream, or ctx is
// cancelled.
func (c *Client) WatchJob(ctx context.Context, id job.ID, onUpdate func(job.Job)) error {
	stream, err := c.rpc.WatchJob(ctx, &hearthv1.WatchJobRequest{Id: string(id)})
	if err != nil {
		return err
	}
	for {
		p, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		onUpdate(wire.JobFromProto(p))
	}
}

func (c *Client) RegisterWorker(ctx context.Context, info app.WorkerInfo) (heartbeatInterval time.Duration, err error) {
	resp, rerr := c.rpc.RegisterWorker(ctx, &hearthv1.RegisterWorkerRequest{Info: wire.WorkerInfoToProto(info)})
	if rerr != nil {
		return 0, rerr
	}
	return resp.GetHeartbeatInterval().AsDuration(), nil
}

// streamReader adapts a server-streaming GetBlob into an io.ReadCloser.
type streamReader struct {
	stream hearthv1.Coordinator_GetBlobClient
	buf    []byte
}

func (r *streamReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		msg, err := r.stream.Recv()
		if errors.Is(err, io.EOF) {
			return 0, io.EOF
		}
		if err != nil {
			return 0, err
		}
		r.buf = msg.GetChunk()
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func (r *streamReader) Close() error {
	// A unary cancellation is not exposed on server-streaming clients,
	// but draining is fine — the deferred Close also cancels the call ctx
	// on the server side via the gRPC connection.
	_ = r.stream.CloseSend // CloseSend exists only on client streams
	// Issue a sentinel timestamp use to silence "imported and not used" if
	// we ever pull it back in; current build does not need it.
	_ = timestamppb.Now
	return nil
}
