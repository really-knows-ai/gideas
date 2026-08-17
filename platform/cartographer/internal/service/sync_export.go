package service

import (
	"context"
	"fmt"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *CartographerServer) HealthCheck(
	ctx context.Context, req *flowv1.HealthCheckRequest,
) (*flowv1.HealthCheckResponse, error) {
	health, err := s.store.Health(ctx)
	if err != nil {
		// No SPEC error-table row names a HealthCheck failure code (the only
		// "before database ready" row is ApplySchema-specific, SPEC error table),
		// so the store probe failure must not be surfaced under a fabricated
		// named code. In particular, routing through mapStoreError would map the
		// ErrDatabaseNotReady sentinel to FAILED_PRECONDITION — a code the table
		// assigns only to ApplySchema, not HealthCheck. Surface the condition
		// generically as INTERNAL (the code the table uses for unexpected
		// service-side failures) instead of letting a raw non-status error cross
		// the RPC boundary as gRPC Unknown. The not-ready state is reported via
		// the response body by the store's own Health (LadybugOk=false), so a
		// probe error here is an unexpected failure and still fails closed.
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &flowv1.HealthCheckResponse{
		LadybugOk: health.LadybugOK, SchemaApplied: health.SchemaApplied, PvcWritable: health.PVCWritable,
	}, nil
}

// =========================================================================
// Administrative Path
// =========================================================================

// Sync wakes the background sync worker and blocks until one full sync cycle
// completes (fetch → merge → re-hydrate → push).
func (s *CartographerServer) Sync(ctx context.Context, req *flowv1.SyncRequest) (*flowv1.SyncResponse, error) {
	// SPEC R10 Sync requires WRITE:graph/entity/*, and the SPEC check-order
	// table lists Sync under "general rule only" — the capability gate. It runs
	// before the remote-configuration probe so an unprivileged caller receives
	// PERMISSION_DENIED regardless of whether a remote is configured; probing
	// remoteURL first would disclose remote-configuration state (FAILED_PRECONDITION
	// vs PERMISSION_DENIED) ahead of any authorization decision. Mirrors
	// BeginTransaction and ExportGraph, which also gate on capability first.
	if err := s.checkWildcardEntityCap(ctx, "WRITE"); err != nil {
		return nil, err
	}
	if s.remoteURL == "" {
		return nil, errRemoteNotConfigured()
	}
	if s.syncWorker == nil {
		return nil, status.Error(codes.Internal, "sync worker not initialised")
	}
	// SPEC R10 Sync: only a non-recoverable cycle error surfaces to the caller
	// ("If the cycle encounters a non-recoverable error, returns the worker's
	// last error"). A recoverable-exhausted cycle (all retries failed) was
	// already logged + telemetry'd by the worker; Sync reports success and the
	// push flag stays set for the next cycle. WithAck callers keep the full
	// error (SPEC R10: an acked commit returns an error whenever the flag is
	// still set).
	completed, class, err := s.syncWorker.WakeAndWaitClassified(ctx)
	if err == nil {
		return &flowv1.SyncResponse{}, nil
	}
	if !completed {
		return nil, status.FromContextError(err).Err()
	}
	if class != syncNonRecoverable {
		return &flowv1.SyncResponse{}, nil
	}
	return nil, mapGitError(err)
}

// ExportGraph streams the serialised graph.
func (s *CartographerServer) ExportGraph(
	req *flowv1.ExportGraphRequest,
	stream grpc.ServerStreamingServer[flowv1.ExportGraphResponse],
) error {
	ctx := stream.Context()
	if err := s.checkWildcardEntityCap(ctx, "READ"); err != nil {
		return err
	}

	// Reject an unsupported format before enumerating the graph. Collecting the
	// full graph only to discard it on an unsupported format wastes I/O (SPEC
	// R11/R2 maps unsupported format to INVALID_ARGUMENT).
	if format := req.GetFormat(); format != ExportFormatJSON && format != ExportFormatGraphML {
		return errUnsupportedExportFormat(format)
	}

	// collectExportData may panic (e.g. bytes.ErrTooLarge, make() OOM) when the
	// in-memory serialisation buffer exceeds available memory, which would crash
	// the process rather than return a controlled error. The recover below converts
	// such panics to RESOURCE_EXHAUSTED, matching SPEC R11 and the error table.
	var data []byte
	var collectErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				collectErr = errExportGraphBufferAllocation(fmt.Sprintf("%v", r))
			}
		}()
		data, collectErr = collectExportData(s, ctx, req.GetFormat())
	}()
	if collectErr != nil {
		return collectErr
	}

	const chunkSize = 1024 * 64
	for i := 0; i < len(data); i += chunkSize {
		end := min(i+chunkSize, len(data))
		if err := stream.Send(&flowv1.ExportGraphResponse{Chunk: data[i:end]}); err != nil {
			return errExportGraphMidStream(err.Error())
		}
	}
	return nil
}
