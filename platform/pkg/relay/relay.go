// Package relay provides the shared server-streaming relay logic for the
// ExportGraph RPC, used identically by the Sidecar CartographerProxy
// (platform/sidecar/internal/proxy) and the Operator's ExportGraph proxy
// (platform/operator/internal/controller). The relay classifies upstream
// failures against the SPEC error table (plans/cartographer/SPEC.md, R11), so
// the mapping lives once here instead of being duplicated across sibling
// modules — duplicated copies of the same wire surface silently diverge.
package relay

import (
	"errors"
	"io"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ExportGraphClient is the read side of the Cartographer's ExportGraph client
// stream the relay consumes. Both generated client stream types
// (flowv1.CartographerService_ExportGraphClient) satisfy it.
type ExportGraphClient interface {
	Recv() (*flowv1.ExportGraphResponse, error)
}

// ExportGraphServer is the write side of the caller-facing ExportGraph server
// stream the relay fills. Both generated server stream types
// (flowv1.CartographerService_ExportGraphServer, grpc.ServerStreamingServer)
// satisfy it.
type ExportGraphServer interface {
	Send(*flowv1.ExportGraphResponse) error
}

// ExportGraph relays the Cartographer's ExportGraph server stream chunk by
// chunk to the caller, applying the SPEC error-table mapping (SPEC R11):
//
//   - "Unsupported export format" → INVALID_ARGUMENT, "ExportGraph buffer
//     allocation failure" → RESOURCE_EXHAUSTED, "ExportGraph stream-establishment
//     failure" → UNAVAILABLE, and the capability rejections "Invalid capability
//     metadata signature" / "Stale capability signature (anti-replay)" / "Read
//     operation without READ capability" → PERMISSION_DENIED are all "no data
//     sent" pre-stream conditions: the Cartographer returns them BEFORE any
//     chunk, so they arrive at the relay's first Recv with no chunk forwarded
//     (a lazy grpc.NewClient dial delivers an unreachable upstream on the first
//     Recv the same way). All four pass through verbatim — preserving the
//     upstream status code and message — so a caller receives the documented
//     error code.
//   - Once at least one chunk has been forwarded, any failure — a transport-level
//     break (Unavailable), a non-conforming upstream status, or a raw error — is
//     a genuine mid-stream failure (partial data may already have been sent) and
//     surfaces as INTERNAL per the SPEC error-table row "ExportGraph mid-stream
//     failure", including a downstream stream break.
func ExportGraph(upstream ExportGraphClient, downstream ExportGraphServer) error {
	var sentAny bool
	for {
		resp, err := upstream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			if !sentAny {
				if st, ok := status.FromError(err); ok {
					switch st.Code() {
					case codes.InvalidArgument, codes.ResourceExhausted, codes.Unavailable, codes.PermissionDenied:
						return err
					}
				}
			}
			return status.Errorf(codes.Internal, "export stream failed: %v", err)
		}
		sentAny = true
		if err := downstream.Send(resp); err != nil {
			return status.Errorf(codes.Internal, "export stream failed: %v", err)
		}
	}
}
