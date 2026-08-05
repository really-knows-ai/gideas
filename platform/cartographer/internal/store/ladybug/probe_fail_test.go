// This file was a "no assertions" debug probe (TestProbeExecuteFailures) that
// printed results, never failed, issued DROP_VECTOR_INDEX without the CALL
// prefix, and discarded results with `_, _ =`. It was removed because the
// extension behaviours it probed are covered by real asserting tests:
// FullTextSearch is covered by TestFullTextSearch_Valid/_CrossType/_EmptyQuery,
// and the dropped-vector-index failure surface by
// TestSearchNeighbors_VectorPrepareFailureSurfacesError. Any future probe here
// must be an asserting test.
package ladybug
