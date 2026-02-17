package proxy

import (
	"context"
	"testing"

	"google.golang.org/grpc/metadata"
)

func TestPropagateMetadata_WithMetadata(t *testing.T) {
	md := metadata.Pairs("x-flow-workitem-id", "test-123", "x-other", "val")
	inCtx := metadata.NewIncomingContext(context.Background(), md)

	outCtx := propagateMetadata(inCtx)

	outMD, ok := metadata.FromOutgoingContext(outCtx)
	if !ok {
		t.Fatal("Expected outgoing metadata")
	}

	vals := outMD.Get("x-flow-workitem-id")
	if len(vals) != 1 || vals[0] != "test-123" {
		t.Fatalf("Expected x-flow-workitem-id=test-123, got %v", vals)
	}

	otherVals := outMD.Get("x-other")
	if len(otherVals) != 1 || otherVals[0] != "val" {
		t.Fatalf("Expected x-other=val, got %v", otherVals)
	}
}

func TestPropagateMetadata_NoMetadata(t *testing.T) {
	ctx := context.Background()
	outCtx := propagateMetadata(ctx)

	// Should return the same context when no incoming metadata exists.
	_, ok := metadata.FromOutgoingContext(outCtx)
	if ok {
		t.Fatal("Expected no outgoing metadata when no incoming metadata")
	}
}
