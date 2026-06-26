package service

import (
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// TestLawGroupMessagesExist guards that the four LawGroup RPC proto messages
// compile and their accessor methods are accessible. This is a compilation
// guard — if any message type or accessor is removed from the proto, this
// test will fail to compile.
func TestLawGroupMessagesExist(t *testing.T) {
	// GetLawGroup
	_ = &flowv1.GetLawGroupRequest{GroupName: "test"}
	_ = &flowv1.GetLawGroupResponse{Group: &flowv1.LawGroup{Name: "test", Mode: "bundle", Passes: 1}}

	// ListLawGroups
	_ = &flowv1.ListLawGroupsRequest{}
	_ = &flowv1.ListLawGroupsResponse{Groups: []*flowv1.LawGroup{{Name: "test", Mode: "bundle", Passes: 1}}}

	// SyncLawGroup
	_ = &flowv1.SyncLawGroupRequest{Group: &flowv1.LawGroup{Name: "test", Mode: "bundle", Passes: 1}}
	_ = &flowv1.SyncLawGroupResponse{Acknowledged: true}

	// DeleteLawGroup
	_ = &flowv1.DeleteLawGroupRequest{GroupName: "test"}
	_ = &flowv1.DeleteLawGroupResponse{Acknowledged: true}
}

// TestLawFilterHasGroupField guards that LawFilter has the Group accessor.
func TestLawFilterHasGroupField(t *testing.T) {
	f := &flowv1.LawFilter{Group: "security"}
	if f.GetGroup() != "security" {
		t.Fatalf("expected LawFilter.Group to be 'security', got %q", f.GetGroup())
	}
}
