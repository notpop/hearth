// Package grpcadapter implements the gRPC transport for Hearth.
//
// The Server side wraps a coordinator + blob store + worker registry and
// is mounted on a *grpc.Server. The Client side (in client.go) is the
// counterpart used by worker runtimes and CLIs. Wire conversion is done
// through the pure-function package internal/wire.
package grpcadapter

import (
	"context"
	"errors"
	"io"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/notpop/hearth/internal/app"
	"github.com/notpop/hearth/internal/app/coordinator"
	"github.com/notpop/hearth/internal/wire"
	"github.com/notpop/hearth/pkg/job"
	hearthv1 "github.com/notpop/hearth/pkg/proto/hearth/v1"
)

// Server implements hearthv1.CoordinatorServer.
type Server struct {
	hearthv1.UnimplementedCoordinatorServer

	coord    *coordinator.Coordinator
	blob     app.BlobStore
	registry app.WorkerRegistry

	heartbeatInterval time.Duration
	watchPoll         time.Duration
	chunkSize         int
}

// ServerOptions configures Server behaviour beyond the wired-in dependencies.
type ServerOptions struct {
	HeartbeatInterval time.Duration // advertised to workers; default 10s
	WatchPoll         time.Duration // WatchJob poll interval; default 200ms
	ChunkSize         int           // GetBlob server-stream chunk size; default 64KiB
}

// NewServer constructs a Server.
func NewServer(coord *coordinator.Coordinator, blob app.BlobStore, registry app.WorkerRegistry, opt ServerOptions) *Server {
	if opt.HeartbeatInterval == 0 {
		opt.HeartbeatInterval = 10 * time.Second
	}
	if opt.WatchPoll == 0 {
		opt.WatchPoll = 200 * time.Millisecond
	}
	if opt.ChunkSize <= 0 {
		opt.ChunkSize = 64 * 1024
	}
	return &Server{
		coord:             coord,
		blob:              blob,
		registry:          registry,
		heartbeatInterval: opt.HeartbeatInterval,
		watchPoll:         opt.WatchPoll,
		chunkSize:         opt.ChunkSize,
	}
}

// --- Client API ---------------------------------------------------------

func (s *Server) SubmitJob(ctx context.Context, req *hearthv1.SubmitJobRequest) (*hearthv1.SubmitJobResponse, error) {
	if req.GetSpec() == nil {
		return nil, status.Error(codes.InvalidArgument, "spec is required")
	}
	id, err := s.coord.Submit(ctx, wire.SpecFromProto(req.GetSpec()))
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "submit: %v", err)
	}
	return &hearthv1.SubmitJobResponse{Id: string(id)}, nil
}

func (s *Server) GetJob(ctx context.Context, req *hearthv1.GetJobRequest) (*hearthv1.Job, error) {
	j, err := s.coord.Get(ctx, job.ID(req.GetId()))
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	return wire.JobToProto(j), nil
}

func (s *Server) ListJobs(ctx context.Context, req *hearthv1.ListJobsRequest) (*hearthv1.ListJobsResponse, error) {
	states := make([]job.State, 0, len(req.GetStates()))
	for _, st := range req.GetStates() {
		states = append(states, wire.StateFromProto(st))
	}
	jobs, err := s.coord.List(ctx, app.ListFilter{
		States: states,
		Kinds:  req.GetKinds(),
		Limit:  int(req.GetLimit()),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	out := make([]*hearthv1.Job, len(jobs))
	for i, j := range jobs {
		out[i] = wire.JobToProto(j)
	}
	return &hearthv1.ListJobsResponse{Jobs: out}, nil
}

func (s *Server) WatchJob(req *hearthv1.WatchJobRequest, stream hearthv1.Coordinator_WatchJobServer) error {
	id := job.ID(req.GetId())

	// Subscribe FIRST so we don't miss the very next transition between
	// the initial snapshot and entering the receive loop. Duplicates are
	// fine — the consumer just re-renders the same state.
	sub := s.coord.Watch(id)
	defer sub.Unsubscribe()

	j, err := s.coord.Get(stream.Context(), id)
	if err != nil {
		return status.Errorf(codes.NotFound, "%v", err)
	}
	if err := stream.Send(wire.JobToProto(j)); err != nil {
		return err
	}
	if j.State.IsTerminal() {
		return nil
	}

	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case upd, ok := <-sub.Updates():
			if !ok {
				return nil
			}
			if err := stream.Send(wire.JobToProto(upd)); err != nil {
				return err
			}
			if upd.State.IsTerminal() {
				return nil
			}
		}
	}
}

// --- Worker API ---------------------------------------------------------

func (s *Server) RegisterWorker(ctx context.Context, req *hearthv1.RegisterWorkerRequest) (*hearthv1.RegisterWorkerResponse, error) {
	info := wire.WorkerInfoFromProto(req.GetInfo())
	if info.LastSeen.IsZero() {
		info.LastSeen = time.Now().UTC()
	}
	if err := s.registry.Register(ctx, info); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &hearthv1.RegisterWorkerResponse{
		HeartbeatInterval: durationpb.New(s.heartbeatInterval),
	}, nil
}

func (s *Server) LeaseJob(ctx context.Context, req *hearthv1.LeaseJobRequest) (*hearthv1.LeaseJobResponse, error) {
	ttl := req.GetLeaseTtl().AsDuration()
	poll := req.GetPollTimeout().AsDuration()
	j, ok, err := s.coord.Lease(ctx, req.GetKinds(), req.GetWorkerId(), ttl, poll)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if !ok {
		return &hearthv1.LeaseJobResponse{}, nil
	}
	return &hearthv1.LeaseJobResponse{Job: wire.JobToProto(j)}, nil
}

func (s *Server) Heartbeat(ctx context.Context, req *hearthv1.HeartbeatRequest) (*hearthv1.HeartbeatResponse, error) {
	expires, cancel, err := s.coord.Heartbeat(ctx,
		job.ID(req.GetJobId()), req.GetWorkerId(),
		wire.ProgressFromProto(req.GetProgress()))
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	resp := &hearthv1.HeartbeatResponse{Cancel: cancel}
	if !expires.IsZero() {
		resp.ExpiresAt = timestamppb.New(expires)
	}
	return resp, nil
}

func (s *Server) CompleteJob(ctx context.Context, req *hearthv1.CompleteJobRequest) (*hearthv1.CompleteJobResponse, error) {
	res := wire.ResultFromProto(req.GetResult())
	if res == nil {
		res = &job.Result{}
	}
	if err := s.coord.Complete(ctx, job.ID(req.GetJobId()), req.GetWorkerId(), *res); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	return &hearthv1.CompleteJobResponse{}, nil
}

func (s *Server) FailJob(ctx context.Context, req *hearthv1.FailJobRequest) (*hearthv1.FailJobResponse, error) {
	will, retryAt, err := s.coord.Fail(ctx, job.ID(req.GetJobId()), req.GetWorkerId(), req.GetErrorMessage())
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	resp := &hearthv1.FailJobResponse{WillRetry: will}
	if !retryAt.IsZero() {
		resp.NextRunAt = timestamppb.New(retryAt)
	}
	return resp, nil
}

// --- Blob API -----------------------------------------------------------

func (s *Server) PutBlob(stream hearthv1.Coordinator_PutBlobServer) error {
	pr, pw := io.Pipe()

	// Pump bytes from the gRPC stream into a Pipe so BlobStore.Put can read
	// it as an io.Reader. Errors on either side surface through CloseWithError.
	go func() {
		var rxErr error
		defer func() {
			_ = pw.CloseWithError(rxErr)
		}()
		for {
			msg, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				rxErr = err
				return
			}
			if chunk := msg.GetChunk(); len(chunk) > 0 {
				if _, err := pw.Write(chunk); err != nil {
					rxErr = err
					return
				}
			}
		}
	}()

	ref, err := s.blob.Put(stream.Context(), pr)
	if err != nil {
		return status.Errorf(codes.Internal, "put blob: %v", err)
	}
	return stream.SendAndClose(&hearthv1.PutBlobResponse{Ref: wire.BlobRefToProto(ref)})
}

func (s *Server) GetBlob(req *hearthv1.GetBlobRequest, stream hearthv1.Coordinator_GetBlobServer) error {
	rc, err := s.blob.Get(stream.Context(), wire.BlobRefFromProto(req.GetRef()))
	if err != nil {
		return status.Errorf(codes.NotFound, "%v", err)
	}
	defer rc.Close()

	buf := make([]byte, s.chunkSize)
	for {
		n, rerr := rc.Read(buf)
		if n > 0 {
			if serr := stream.Send(&hearthv1.GetBlobResponse{Chunk: append([]byte(nil), buf[:n]...)}); serr != nil {
				return serr
			}
		}
		if errors.Is(rerr, io.EOF) {
			return nil
		}
		if rerr != nil {
			return status.Errorf(codes.Internal, "%v", rerr)
		}
	}
}

func (s *Server) HasBlob(ctx context.Context, req *hearthv1.HasBlobRequest) (*hearthv1.HasBlobResponse, error) {
	ok, err := s.blob.Has(ctx, wire.BlobRefFromProto(req.GetRef()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &hearthv1.HasBlobResponse{Present: ok}, nil
}

func (s *Server) CancelJob(ctx context.Context, req *hearthv1.CancelJobRequest) (*hearthv1.CancelJobResponse, error) {
	if err := s.coord.Cancel(ctx, job.ID(req.GetId())); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	return &hearthv1.CancelJobResponse{}, nil
}

// --- Admin --------------------------------------------------------------

func (s *Server) ListNodes(ctx context.Context, _ *hearthv1.ListNodesRequest) (*hearthv1.ListNodesResponse, error) {
	nodes, err := s.registry.List(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	out := make([]*hearthv1.WorkerInfo, len(nodes))
	for i, n := range nodes {
		out[i] = wire.WorkerInfoToProto(n)
	}
	return &hearthv1.ListNodesResponse{Nodes: out}, nil
}
