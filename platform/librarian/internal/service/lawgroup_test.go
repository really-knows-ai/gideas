package service

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowmeta "github.com/foundry/flow/pkg/metadata"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// LawGroup RPCs
// ---------------------------------------------------------------------------

func TestGetLawGroup_StoredGroup(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Sync a group first.
	_, err := srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{
			Name: "security", Mode: "law-by-law", Passes: 3,
		},
	})
	if err != nil {
		t.Fatalf("SyncLawGroup: %v", err)
	}

	resp, err := srv.GetLawGroup(ctx, &flowv1.GetLawGroupRequest{GroupName: "security"})
	if err != nil {
		t.Fatalf("GetLawGroup: %v", err)
	}
	g := resp.GetGroup()
	if g.GetName() != "security" {
		t.Fatalf("expected name %q, got %q", "security", g.GetName())
	}
	if g.GetMode() != testLawByLawMode {
		t.Fatalf("expected mode %q, got %q", testLawByLawMode, g.GetMode())
	}
	if g.GetPasses() != 3 {
		t.Fatalf("expected passes 3, got %d", g.GetPasses())
	}
}

func TestGetLawGroup_UnknownGroupReturnsDefault(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.GetLawGroup(ctx, &flowv1.GetLawGroupRequest{GroupName: "nonexistent"})
	if err != nil {
		t.Fatalf("GetLawGroup for unknown group: %v", err)
	}
	g := resp.GetGroup()
	if g.GetName() != "nonexistent" {
		t.Fatalf("expected name %q, got %q", "nonexistent", g.GetName())
	}
	if g.GetMode() != testBundleMode {
		t.Fatalf("expected default mode %q, got %q", testBundleMode, g.GetMode())
	}
	if g.GetPasses() != 1 {
		t.Fatalf("expected default passes 1, got %d", g.GetPasses())
	}
}

func TestGetLawGroup_EmptyName(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.GetLawGroup(ctx, &flowv1.GetLawGroupRequest{})
	if err != nil {
		t.Fatalf("GetLawGroup for empty name: %v", err)
	}
	g := resp.GetGroup()
	if g.GetName() != "" {
		t.Fatalf("expected empty name, got %q", g.GetName())
	}
	if g.GetMode() != testBundleMode {
		t.Fatalf("expected default mode %q, got %q", testBundleMode, g.GetMode())
	}
	if g.GetPasses() != 1 {
		t.Fatalf("expected default passes 1, got %d", g.GetPasses())
	}
}

func TestGetLawGroup_CapabilityDenied(t *testing.T) {
	srv := newTestServer(t)
	md := metadata.Pairs(flowmeta.MetadataKeyCapabilities, "WRITE:law", flowmeta.MetadataKeyNodeID, "node-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.GetLawGroup(ctx, &flowv1.GetLawGroupRequest{GroupName: "security"})
	if err == nil {
		t.Fatal("expected PermissionDenied, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestListLawGroups_ReturnsStored(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, _ = srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{Name: "group-a", Mode: "bundle", Passes: 1},
	})
	_, _ = srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{Name: "group-b", Mode: "law-by-law", Passes: 2},
	})

	resp, err := srv.ListLawGroups(ctx, &flowv1.ListLawGroupsRequest{})
	if err != nil {
		t.Fatalf("ListLawGroups: %v", err)
	}
	if len(resp.GetGroups()) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(resp.GetGroups()))
	}
	names := map[string]bool{}
	for _, g := range resp.GetGroups() {
		names[g.GetName()] = true
	}
	if !names["group-a"] || !names["group-b"] {
		t.Fatalf("expected group-a and group-b, got %v", names)
	}
}

func TestListLawGroups_Empty(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.ListLawGroups(ctx, &flowv1.ListLawGroupsRequest{})
	if err != nil {
		t.Fatalf("ListLawGroups: %v", err)
	}
	if len(resp.GetGroups()) != 0 {
		t.Fatalf("expected 0 groups, got %d", len(resp.GetGroups()))
	}
}

func TestListLawGroups_CapabilityDenied(t *testing.T) {
	srv := newTestServer(t)
	md := metadata.Pairs(flowmeta.MetadataKeyCapabilities, "WRITE:law", flowmeta.MetadataKeyNodeID, "node-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.ListLawGroups(ctx, &flowv1.ListLawGroupsRequest{})
	if err == nil {
		t.Fatal("expected PermissionDenied, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestSyncLawGroup_InsertAndGet(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{Name: "security", Mode: "law-by-law", Passes: 3},
	})
	if err != nil {
		t.Fatalf("SyncLawGroup: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	// Verify via GetLawGroup.
	getResp, err := srv.GetLawGroup(ctx, &flowv1.GetLawGroupRequest{GroupName: "security"})
	if err != nil {
		t.Fatalf("GetLawGroup after SyncLawGroup: %v", err)
	}
	if getResp.GetGroup().GetPasses() != 3 {
		t.Fatalf("expected passes 3, got %d", getResp.GetGroup().GetPasses())
	}
}

func TestSyncLawGroup_UpdateExisting(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, _ = srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{Name: "security", Mode: "bundle", Passes: 1},
	})
	_, err := srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{Name: "security", Mode: "law-by-law", Passes: 5},
	})
	if err != nil {
		t.Fatalf("SyncLawGroup update: %v", err)
	}

	getResp, _ := srv.GetLawGroup(ctx, &flowv1.GetLawGroupRequest{GroupName: "security"})
	if getResp.GetGroup().GetMode() != "law-by-law" {
		t.Fatalf("expected mode %q, got %q", "law-by-law", getResp.GetGroup().GetMode())
	}
	if getResp.GetGroup().GetPasses() != 5 {
		t.Fatalf("expected passes 5, got %d", getResp.GetGroup().GetPasses())
	}
}

func TestSyncLawGroup_EmptyName(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{Mode: "bundle", Passes: 1},
	})
	if err == nil {
		t.Fatal("expected InvalidArgument for empty name")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestSyncLawGroup_InvalidMode(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{Name: "test", Mode: "invalid", Passes: 1},
	})
	if err == nil {
		t.Fatal("expected InvalidArgument for invalid mode")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestSyncLawGroup_PassesLessThanOne(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{Name: "test", Mode: "bundle", Passes: 0},
	})
	if err == nil {
		t.Fatal("expected InvalidArgument for passes < 1")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestSyncLawGroup_CapabilityDenied(t *testing.T) {
	srv := newTestServer(t)
	md := metadata.Pairs(flowmeta.MetadataKeyCapabilities, "READ:law", flowmeta.MetadataKeyNodeID, "node-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{Name: "test", Mode: "bundle", Passes: 1},
	})
	if err == nil {
		t.Fatal("expected PermissionDenied, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestDeleteLawGroup_RemovesGroup(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, _ = srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{Name: "security", Mode: "bundle", Passes: 1},
	})

	resp, err := srv.DeleteLawGroup(ctx, &flowv1.DeleteLawGroupRequest{GroupName: "security"})
	if err != nil {
		t.Fatalf("DeleteLawGroup: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	// After delete, GetLawGroup returns the built-in default.
	getResp, err := srv.GetLawGroup(ctx, &flowv1.GetLawGroupRequest{GroupName: "security"})
	if err != nil {
		t.Fatalf("GetLawGroup after delete: %v", err)
	}
	if getResp.GetGroup().GetMode() != "bundle" {
		t.Fatalf("expected default mode after delete, got %q", getResp.GetGroup().GetMode())
	}
}

func TestDeleteLawGroup_NonExistent(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.DeleteLawGroup(ctx, &flowv1.DeleteLawGroupRequest{GroupName: "nonexistent"})
	if err == nil {
		t.Fatal("expected NotFound for non-existent group")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestDeleteLawGroup_EmptyName(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.DeleteLawGroup(ctx, &flowv1.DeleteLawGroupRequest{})
	if err == nil {
		t.Fatal("expected InvalidArgument for empty name")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestDeleteLawGroup_CapabilityDenied(t *testing.T) {
	srv := newTestServer(t)
	md := metadata.Pairs(flowmeta.MetadataKeyCapabilities, "READ:law", flowmeta.MetadataKeyNodeID, "node-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.DeleteLawGroup(ctx, &flowv1.DeleteLawGroupRequest{GroupName: "test"})
	if err == nil {
		t.Fatal("expected PermissionDenied, got nil")
	}
}
