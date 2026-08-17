package service

import (
	"context"
	"log/slog"
	"strings"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flow "github.com/foundry/flow/sdk/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetLawGroup returns a single LawGroup by name. If the group has no stored
// entry, a built-in default {mode:"bundle", passes:1} is returned without error.
func (s *LibrarianServer) GetLawGroup(
	ctx context.Context, req *flowv1.GetLawGroupRequest,
) (*flowv1.GetLawGroupResponse, error) {
	if err := flow.CheckCapability(ctx, "READ:law"); err != nil {
		return nil, err
	}

	group, err := s.store.GetLawGroup(ctx, req.GetGroupName())
	if err != nil {
		// "not found" → return built-in default without error.
		// Any other store error → propagate as Internal.
		if strings.Contains(err.Error(), "not found") {
			return &flowv1.GetLawGroupResponse{
				Group: &flowv1.LawGroup{
					Name:   req.GetGroupName(),
					Mode:   "bundle",
					Passes: 1,
				},
			}, nil
		}
		return nil, status.Errorf(codes.Internal, "get law group: %v", err)
	}
	return &flowv1.GetLawGroupResponse{Group: storeLawGroupToProto(group)}, nil
}

// ListLawGroups returns all stored law groups. Does NOT include the built-in
// default for groups without entries.
func (s *LibrarianServer) ListLawGroups(
	ctx context.Context, req *flowv1.ListLawGroupsRequest,
) (*flowv1.ListLawGroupsResponse, error) {
	if err := flow.CheckCapability(ctx, "READ:law"); err != nil {
		return nil, err
	}

	groups, err := s.store.ListLawGroups(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list law groups: %v", err)
	}
	proto := make([]*flowv1.LawGroup, 0, len(groups))
	for _, g := range groups {
		proto = append(proto, storeLawGroupToProto(g))
	}
	return &flowv1.ListLawGroupsResponse{Groups: proto}, nil
}

// SyncLawGroup upserts a LawGroup from the Operator's CRD watch sync.
func (s *LibrarianServer) SyncLawGroup(
	ctx context.Context, req *flowv1.SyncLawGroupRequest,
) (*flowv1.SyncLawGroupResponse, error) {
	if err := flow.CheckCapability(ctx, "WRITE:law"); err != nil {
		return nil, err
	}

	g := req.GetGroup()
	if g.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "group name is required")
	}
	if g.GetMode() != "bundle" && g.GetMode() != "law-by-law" {
		return nil, status.Error(codes.InvalidArgument, "mode must be 'bundle' or 'law-by-law'")
	}
	if g.GetPasses() < 1 {
		return nil, status.Error(codes.InvalidArgument, "passes must be >= 1")
	}

	if err := s.store.UpsertLawGroup(ctx, g.GetName(), g.GetMode(), int(g.GetPasses())); err != nil {
		slog.Error("SyncLawGroup failed", "group", g.GetName(), "error", err)
		return nil, status.Errorf(codes.Internal, "sync law group: %v", err)
	}

	slog.Info("SyncLawGroup", "group", g.GetName(), "mode", g.GetMode(), "passes", g.GetPasses())
	return &flowv1.SyncLawGroupResponse{Acknowledged: true}, nil
}

// DeleteLawGroup removes a LawGroup from the Librarian store by name.
func (s *LibrarianServer) DeleteLawGroup(
	ctx context.Context, req *flowv1.DeleteLawGroupRequest,
) (*flowv1.DeleteLawGroupResponse, error) {
	if err := flow.CheckCapability(ctx, "WRITE:law"); err != nil {
		return nil, err
	}

	if req.GetGroupName() == "" {
		return nil, status.Error(codes.InvalidArgument, "group name is required")
	}

	if err := s.store.DeleteLawGroup(ctx, req.GetGroupName()); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "law group %q not found", req.GetGroupName())
		}
		slog.Error("DeleteLawGroup failed", "group", req.GetGroupName(), "error", err)
		return nil, status.Errorf(codes.Internal, "delete law group: %v", err)
	}

	slog.Info("DeleteLawGroup", "group", req.GetGroupName())
	return &flowv1.DeleteLawGroupResponse{Acknowledged: true}, nil
}
