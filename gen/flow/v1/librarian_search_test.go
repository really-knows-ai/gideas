package flowv1_test

import (
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// TestLibrarianServiceSearchSimilarLawsPresent is an architectural guard that
// ensures the SearchSimilarLaws RPC (added in Phase 13) has not been removed
// from the librarian.proto service descriptor.
func TestLibrarianServiceSearchSimilarLawsPresent(t *testing.T) {
	t.Parallel()

	desc := flowv1.LibrarianService_ServiceDesc
	methods := make(map[string]bool)
	for _, m := range desc.Methods {
		methods[m.MethodName] = true
	}
	for _, s := range desc.Streams {
		methods[s.StreamName] = true
	}

	if !methods["SearchSimilarLaws"] {
		t.Fatal("LibrarianService_ServiceDesc missing required RPC SearchSimilarLaws")
	}
}

// TestLibrarianServiceSearchSimilarLawsFullMethodConst verifies that the
// generated full-method constant for SearchSimilarLaws exists and has the
// expected value. This is a compile-time + value guard.
func TestLibrarianServiceSearchSimilarLawsFullMethodConst(t *testing.T) {
	t.Parallel()

	const want = "/flow.v1.LibrarianService/SearchSimilarLaws"
	if flowv1.LibrarianService_SearchSimilarLaws_FullMethodName != want {
		t.Fatalf("SearchSimilarLaws full method name = %q, want %q",
			flowv1.LibrarianService_SearchSimilarLaws_FullMethodName, want)
	}
}
