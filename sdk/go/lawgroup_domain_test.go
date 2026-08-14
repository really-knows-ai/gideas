package flow

import (
	"context"
	"fmt"
	"net"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// ---------------------------------------------------------------------------
// Spy types for LawGroup/Law domain method tests
// ---------------------------------------------------------------------------

// librarianSpy implements flowv1.LibrarianServiceServer for testing.
// It captures QueryLaws and Cite requests and returns configurable responses.
type librarianSpy struct {
	flowv1.UnimplementedLibrarianServiceServer
	lastQueryLawsReq *flowv1.QueryLawsRequest
	lastCiteReq      *flowv1.CiteRequest
	queryLawsResult  *flowv1.QueryLawsResponse
	queryLawsErr     error
	citeResult       *flowv1.CiteResponse
	citeErr          error
}

func (s *librarianSpy) QueryLaws(ctx context.Context, req *flowv1.QueryLawsRequest) (*flowv1.QueryLawsResponse, error) {
	s.lastQueryLawsReq = req
	if s.queryLawsErr != nil {
		return nil, s.queryLawsErr
	}
	if s.queryLawsResult != nil {
		return s.queryLawsResult, nil
	}
	return &flowv1.QueryLawsResponse{}, nil
}

func (s *librarianSpy) Cite(ctx context.Context, req *flowv1.CiteRequest) (*flowv1.CiteResponse, error) {
	s.lastCiteReq = req
	if s.citeErr != nil {
		return nil, s.citeErr
	}
	if s.citeResult != nil {
		return s.citeResult, nil
	}
	return &flowv1.CiteResponse{}, nil
}

// archivistSpy implements flowv1.ArchivistServiceServer for testing.
// It captures StampArtefact requests and returns configurable responses.
type archivistSpy struct {
	flowv1.UnimplementedArchivistServiceServer
	lastStampReq *flowv1.StampArtefactRequest
	stampErr     error
}

func (s *archivistSpy) StampArtefact(
	ctx context.Context, req *flowv1.StampArtefactRequest,
) (*flowv1.StampArtefactResponse, error) {
	s.lastStampReq = req
	if s.stampErr != nil {
		return nil, s.stampErr
	}
	return &flowv1.StampArtefactResponse{}, nil
}

// ---------------------------------------------------------------------------
// bufconn helpers
// ---------------------------------------------------------------------------

// dialBufconn creates a bufconn gRPC server, registers services via register,
// and returns a connected *grpc.ClientConn. Cleanup is registered via t.Cleanup.
func dialBufconn(t *testing.T, register func(srv *grpc.Server)) *grpc.ClientConn {
	t.Helper()

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	register(srv)

	go func() {
		_ = srv.Serve(lis)
	}()

	t.Cleanup(func() { srv.GracefulStop() })

	conn, err := grpc.NewClient("passthrough:///",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial bufconn: %v", err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

// newLibrarianClient connects to the given spy and returns a real gRPC client.
func newLibrarianClient(t *testing.T, spy *librarianSpy) flowv1.LibrarianServiceClient {
	t.Helper()
	conn := dialBufconn(t, func(srv *grpc.Server) {
		flowv1.RegisterLibrarianServiceServer(srv, spy)
	})
	return flowv1.NewLibrarianServiceClient(conn)
}

// newArchivistClient connects to the given spy and returns a real gRPC client.
func newArchivistClient(t *testing.T, spy *archivistSpy) flowv1.ArchivistServiceClient {
	t.Helper()
	conn := dialBufconn(t, func(srv *grpc.Server) {
		flowv1.RegisterArchivistServiceServer(srv, spy)
	})
	return flowv1.NewArchivistServiceClient(conn)
}

// ---------------------------------------------------------------------------
// LawGroup getter tests (Round 1)
// ---------------------------------------------------------------------------

func TestLawGroup_Name(t *testing.T) {
	lg := newLawGroup("test-group", GroupModeBundle, 1, nil)
	if lg.Name() != "test-group" {
		t.Fatalf("expected Name()=test-group, got %q", lg.Name())
	}
}

func TestLawGroup_Mode(t *testing.T) {
	lg := newLawGroup("g", GroupModeBundle, 1, nil)
	if lg.Mode() != GroupModeBundle {
		t.Fatalf("expected Mode()=bundle, got %q", lg.Mode())
	}

	lg2 := newLawGroup("g", GroupModeLawByLaw, 1, nil)
	if lg2.Mode() != GroupModeLawByLaw {
		t.Fatalf("expected Mode()=law-by-law, got %q", lg2.Mode())
	}
}

func TestLawGroup_ZeroValue(t *testing.T) {
	lg := newLawGroup("", "", 0, nil)
	if lg.Name() != "" {
		t.Fatalf("expected empty Name(), got %q", lg.Name())
	}
	if lg.Mode() != "" {
		t.Fatalf("expected empty Mode(), got %q", lg.Mode())
	}
}

// ---------------------------------------------------------------------------
// LawGroup GetLaws tests (Round 2)
// ---------------------------------------------------------------------------

func TestLawGroup_GetLaws_ReturnsLaws(t *testing.T) {
	spy := &librarianSpy{
		queryLawsResult: &flowv1.QueryLawsResponse{
			Laws: []*flowv1.Law{
				{Id: lawL001, Goal: "law one"},
				{Id: lawL002, Goal: "law two"},
			},
		},
	}
	librarian := newLibrarianClient(t, spy)
	lg := newLawGroup("security", GroupModeBundle, 1, librarian)

	laws, err := lg.GetLaws()
	if err != nil {
		t.Fatalf("GetLaws() returned error: %v", err)
	}
	if len(laws) != 2 {
		t.Fatalf("expected 2 laws, got %d", len(laws))
	}
	if laws[0].ID() != lawL001 {
		t.Fatalf("expected laws[0].ID()=L001, got %q", laws[0].ID())
	}
	if laws[1].ID() != lawL002 {
		t.Fatalf("expected laws[1].ID()=L002, got %q", laws[1].ID())
	}

	// Verify the group filter was sent.
	if spy.lastQueryLawsReq == nil {
		t.Fatal("QueryLaws was not called")
	}
	if spy.lastQueryLawsReq.GetFilter().GetGroup() != "security" {
		t.Fatalf("expected filter group=security, got %q", spy.lastQueryLawsReq.GetFilter().GetGroup())
	}
}

func TestLawGroup_GetLaws_EmptyGroup(t *testing.T) {
	spy := &librarianSpy{
		queryLawsResult: &flowv1.QueryLawsResponse{Laws: []*flowv1.Law{}},
	}
	librarian := newLibrarianClient(t, spy)
	lg := newLawGroup("empty-group", GroupModeBundle, 1, librarian)

	laws, err := lg.GetLaws()
	if err != nil {
		t.Fatalf("GetLaws() returned error: %v", err)
	}
	if laws == nil {
		t.Fatal("expected empty slice, got nil")
	}
	if len(laws) != 0 {
		t.Fatalf("expected 0 laws, got %d", len(laws))
	}
}

func TestLawGroup_GetLaws_ServerError(t *testing.T) {
	spy := &librarianSpy{
		queryLawsErr: fmt.Errorf("librarian unavailable"),
	}
	librarian := newLibrarianClient(t, spy)
	lg := newLawGroup("broken", GroupModeBundle, 1, librarian)

	_, err := lg.GetLaws()
	if err == nil {
		t.Fatal("expected error from GetLaws(), got nil")
	}
}

// ---------------------------------------------------------------------------
// LawGroup Attest tests (Round 3)
// ---------------------------------------------------------------------------

func TestLawGroup_Attest_StampsCorrectName(t *testing.T) {
	archSpy := &archivistSpy{}
	archivist := newArchivistClient(t, archSpy)

	art := &Artefact{
		artefactID:       "test-art",
		governedArtefact: "test-gov",
		session: &session{
			workitemID: "test-wid",
			Archivist:  archivist,
		},
	}

	lg := newLawGroup("security", GroupModeBundle, 1, nil)
	err := lg.Attest(art)
	if err != nil {
		t.Fatalf("Attest() returned error: %v", err)
	}

	if archSpy.lastStampReq == nil {
		t.Fatal("StampArtefact was not called")
	}
	if archSpy.lastStampReq.GetStampName() != "lawgrp-security" {
		t.Fatalf("expected stamp name lawgrp-security, got %q", archSpy.lastStampReq.GetStampName())
	}
}

func TestLawGroup_Attest_EmptyGroupName(t *testing.T) {
	archSpy := &archivistSpy{}
	archivist := newArchivistClient(t, archSpy)

	art := &Artefact{
		artefactID:       "test-art",
		governedArtefact: "test-gov",
		session: &session{
			workitemID: "test-wid",
			Archivist:  archivist,
		},
	}

	lg := newLawGroup("", GroupModeBundle, 1, nil)
	err := lg.Attest(art)
	if err != nil {
		t.Fatalf("Attest() returned error: %v", err)
	}

	if archSpy.lastStampReq.GetStampName() != "lawgrp-" {
		t.Fatalf("expected stamp name lawgrp-, got %q", archSpy.lastStampReq.GetStampName())
	}
}

func TestLawGroup_Attest_ArchivistError(t *testing.T) {
	archSpy := &archivistSpy{
		stampErr: fmt.Errorf("archivist error: stamp failed"),
	}
	archivist := newArchivistClient(t, archSpy)

	art := &Artefact{
		artefactID:       "test-art",
		governedArtefact: "test-gov",
		session: &session{
			workitemID: "test-wid",
			Archivist:  archivist,
		},
	}

	lg := newLawGroup("security", GroupModeBundle, 1, nil)
	err := lg.Attest(art)
	if err == nil {
		t.Fatal("expected error from Attest(), got nil")
	}
}

// ---------------------------------------------------------------------------
// Law getter tests (Round 4)
// ---------------------------------------------------------------------------

func TestLaw_ID(t *testing.T) {
	law := newLaw(&flowv1.Law{Id: lawL001}, nil)
	if law.ID() != lawL001 {
		t.Fatalf("expected ID()=L001, got %q", law.ID())
	}
}

func TestLaw_GetGoal(t *testing.T) {
	law := newLaw(&flowv1.Law{Goal: "ensure security"}, nil)
	if law.GetGoal() != "ensure security" {
		t.Fatalf("expected GetGoal()='ensure security', got %q", law.GetGoal())
	}
}

func TestLaw_GetTier(t *testing.T) {
	tests := []struct {
		tier flowv1.LawTier
		want int32
	}{
		{flowv1.LawTier_LAW_TIER_UNSPECIFIED, 0},
		{flowv1.LawTier_LAW_TIER_FINDING, 1},
		{flowv1.LawTier_LAW_TIER_RULING, 2},
		{flowv1.LawTier_LAW_TIER_LOCAL_STATUTE, 3},
		{flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION, 4},
		{flowv1.LawTier_LAW_TIER_FEDERAL_ACCORD, 5},
	}
	for _, tt := range tests {
		law := newLaw(&flowv1.Law{Tier: tt.tier}, nil)
		if got := law.GetTier(); got != tt.want {
			t.Errorf("GetTier() for %v = %d, want %d", tt.tier, got, tt.want)
		}
	}
}

func TestLaw_GetRepresentations(t *testing.T) {
	reps := []*flowv1.Representation{
		{Type: "text/markdown", Content: "# doc"},
		{Type: "text/plain", Content: "plain doc"},
	}
	law := newLaw(&flowv1.Law{Representations: reps}, nil)
	got := law.GetRepresentations()
	if len(got) != 2 {
		t.Fatalf("expected 2 representations, got %d", len(got))
	}
	if got[0].GetType() != "text/markdown" {
		t.Fatalf("expected rep[0].type=text/markdown, got %q", got[0].GetType())
	}
	if got[1].GetType() != "text/plain" {
		t.Fatalf("expected rep[1].type=text/plain, got %q", got[1].GetType())
	}
}

func TestLaw_NilProto(t *testing.T) {
	law := newLaw(nil, nil)
	if law.ID() != "" {
		t.Fatalf("expected empty ID() for nil proto, got %q", law.ID())
	}
	if law.GetGoal() != "" {
		t.Fatalf("expected empty GetGoal() for nil proto, got %q", law.GetGoal())
	}
	if law.GetTier() != 0 {
		t.Fatalf("expected GetTier()=0 for nil proto, got %d", law.GetTier())
	}
	if law.GetRepresentations() != nil {
		t.Fatalf("expected nil GetRepresentations() for nil proto")
	}
}

func TestLaw_PB(t *testing.T) {
	pb := &flowv1.Law{Id: lawL001}
	law := newLaw(pb, nil)
	if law.PB() != pb {
		t.Fatal("PB() should return the same pointer")
	}
	if law.PB().GetId() != lawL001 {
		t.Fatalf("expected PB().GetId()=L001, got %q", law.PB().GetId())
	}

	nilLaw := newLaw(nil, nil)
	if nilLaw.PB() != nil {
		t.Fatal("PB() should return nil for nil proto")
	}
}

// ---------------------------------------------------------------------------
// Law Attest tests (Round 6)
// ---------------------------------------------------------------------------

func TestLaw_Attest_StampsCorrectName(t *testing.T) {
	archSpy := &archivistSpy{}
	archivist := newArchivistClient(t, archSpy)

	art := &Artefact{
		artefactID:       "test-art",
		governedArtefact: "test-gov",
		session: &session{
			workitemID: "test-wid",
			Archivist:  archivist,
		},
	}

	law := newLaw(&flowv1.Law{Id: lawL001}, nil)
	err := law.Attest(art, "text/markdown")
	if err != nil {
		t.Fatalf("Attest() returned error: %v", err)
	}

	if archSpy.lastStampReq == nil {
		t.Fatal("StampArtefact was not called")
	}
	if archSpy.lastStampReq.GetStampName() != "law-L001-text-markdown" {
		t.Fatalf("expected stamp name law-L001-text-markdown, got %q", archSpy.lastStampReq.GetStampName())
	}
}

func TestLaw_Attest_PlainType(t *testing.T) {
	archSpy := &archivistSpy{}
	archivist := newArchivistClient(t, archSpy)

	art := &Artefact{
		artefactID:       "test-art",
		governedArtefact: "test-gov",
		session: &session{
			workitemID: "test-wid",
			Archivist:  archivist,
		},
	}

	law := newLaw(&flowv1.Law{Id: lawL001}, nil)
	err := law.Attest(art, "text/plain")
	if err != nil {
		t.Fatalf("Attest() returned error: %v", err)
	}

	if archSpy.lastStampReq.GetStampName() != "law-L001-text-plain" {
		t.Fatalf("expected stamp name law-L001-text-plain, got %q", archSpy.lastStampReq.GetStampName())
	}
}

func TestLaw_Attest_EmptyRepType(t *testing.T) {
	archSpy := &archivistSpy{}
	archivist := newArchivistClient(t, archSpy)

	art := &Artefact{
		artefactID:       "test-art",
		governedArtefact: "test-gov",
		session: &session{
			workitemID: "test-wid",
			Archivist:  archivist,
		},
	}

	law := newLaw(&flowv1.Law{Id: lawL001}, nil)
	err := law.Attest(art, "")
	if err != nil {
		t.Fatalf("Attest() returned error: %v", err)
	}

	if archSpy.lastStampReq.GetStampName() != "law-L001-" {
		t.Fatalf("expected stamp name law-L001-, got %q", archSpy.lastStampReq.GetStampName())
	}
}

func TestLaw_Attest_ArchivistError(t *testing.T) {
	archSpy := &archivistSpy{
		stampErr: fmt.Errorf("archivist error: stamp failed"),
	}
	archivist := newArchivistClient(t, archSpy)

	art := &Artefact{
		artefactID:       "test-art",
		governedArtefact: "test-gov",
		session: &session{
			workitemID: "test-wid",
			Archivist:  archivist,
		},
	}

	law := newLaw(&flowv1.Law{Id: lawL001}, nil)
	err := law.Attest(art, "text/markdown")
	if err == nil {
		t.Fatal("expected error from Attest(), got nil")
	}
}
