package proxy

import (
	"context"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// captureLibrarianServer captures Librarian RPC calls for assertions.
type captureLibrarianServer struct {
	flowv1.UnimplementedLibrarianServiceServer
	lastCiteReq              *flowv1.CiteRequest
	lastQueryReq             *flowv1.QueryLawsRequest
	lastGetLawReq            *flowv1.GetLawRequest
	lastGetActiveDisputesReq *flowv1.GetActiveDisputesRequest
	lastSearchSimilarReq     *flowv1.SearchSimilarLawsRequest
	capturedMD               metadata.MD
}

func (s *captureLibrarianServer) QueryLaws(
	ctx context.Context, req *flowv1.QueryLawsRequest,
) (*flowv1.QueryLawsResponse, error) {
	s.lastQueryReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.QueryLawsResponse{}, nil
}

func (s *captureLibrarianServer) Cite(ctx context.Context, req *flowv1.CiteRequest) (*flowv1.CiteResponse, error) {
	s.lastCiteReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.CiteResponse{Acknowledged: true}, nil
}

func (s *captureLibrarianServer) GetLaw(
	ctx context.Context, req *flowv1.GetLawRequest,
) (*flowv1.GetLawResponse, error) {
	s.lastGetLawReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.GetLawResponse{Law: &flowv1.Law{Id: req.GetLawId()}}, nil
}

func (s *captureLibrarianServer) GetActiveDisputes(
	ctx context.Context, req *flowv1.GetActiveDisputesRequest,
) (*flowv1.GetActiveDisputesResponse, error) {
	s.lastGetActiveDisputesReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.GetActiveDisputesResponse{
		Records: []*flowv1.DisputeRecord{
			{PetitionId: "pet-1", CitedLawIds: []string{"law-a"}},
		},
	}, nil
}

func (s *captureLibrarianServer) SearchSimilarLaws(
	ctx context.Context, req *flowv1.SearchSimilarLawsRequest,
) (*flowv1.SearchSimilarLawsResponse, error) {
	s.lastSearchSimilarReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.SearchSimilarLawsResponse{
		Results: []*flowv1.SimilarLaw{
			{
				Law:             &flowv1.Law{Id: "law-similar-1"},
				SimilarityScore: 0.92,
			},
		},
	}, nil
}

type librarianTestEnv struct {
	proxy        *LibrarianProxy
	librarianSpy *captureLibrarianServer
}

func setupLibrarianProxy(t *testing.T) *librarianTestEnv {
	t.Helper()

	libSpy := &captureLibrarianServer{}
	libConn := dialBufconn(t, func(s *grpc.Server) {
		flowv1.RegisterLibrarianServiceServer(s, libSpy)
	})

	p := &LibrarianProxy{
		client:          flowv1.NewLibrarianServiceClient(libConn),
		telemetryBuffer: nil,
		conn:            libConn,
		magnitude:       1,
	}

	return &librarianTestEnv{
		proxy:        p,
		librarianSpy: libSpy,
	}
}

func TestLibrarianProxy_Cite_ForwardsToLibrarian(t *testing.T) {
	env := setupLibrarianProxy(t)

	md := metadata.Pairs(
		"x-flow-workitem-id", "wi-test",
		"x-flow-namespace", "ns-test",
		"x-flow-node-id", "node-test",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := env.proxy.Cite(ctx, &flowv1.CiteRequest{
		LawIds: []string{"law-1", "law-2"},
	})
	if err != nil {
		t.Fatalf("Cite: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	// Verify Cite was forwarded to Librarian.
	if env.librarianSpy.lastCiteReq == nil {
		t.Fatal("Cite was not forwarded to Librarian")
	}
	if len(env.librarianSpy.lastCiteReq.GetLawIds()) != 2 {
		t.Fatalf("expected 2 law_ids forwarded, got %d", len(env.librarianSpy.lastCiteReq.GetLawIds()))
	}
}

func TestLibrarianProxy_QueryLaws_PropagatesMetadata(t *testing.T) {
	env := setupLibrarianProxy(t)

	md := metadata.Pairs("x-flow-workitem-id", "wi-meta-test")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := env.proxy.QueryLaws(ctx, &flowv1.QueryLawsRequest{})
	if err != nil {
		t.Fatalf("QueryLaws: %v", err)
	}

	vals := env.librarianSpy.capturedMD.Get("x-flow-workitem-id")
	if len(vals) != 1 || vals[0] != "wi-meta-test" {
		t.Fatalf("expected metadata propagation, got %v", vals)
	}
}

func TestLibrarianProxy_GetLaw_Passthrough(t *testing.T) {
	env := setupLibrarianProxy(t)

	resp, err := env.proxy.GetLaw(context.Background(), &flowv1.GetLawRequest{LawId: "law-123"})
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	if resp.GetLaw().GetId() != "law-123" {
		t.Fatalf("expected law_id=law-123, got %q", resp.GetLaw().GetId())
	}
}

func TestLibrarianProxy_GetActiveDisputes_Passthrough(t *testing.T) {
	env := setupLibrarianProxy(t)

	md := metadata.Pairs("x-flow-workitem-id", "wi-dispute-test")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := env.proxy.GetActiveDisputes(ctx, &flowv1.GetActiveDisputesRequest{
		LawId: "law-a",
	})
	if err != nil {
		t.Fatalf("GetActiveDisputes: %v", err)
	}

	// Verify request was forwarded to Librarian backend.
	if env.librarianSpy.lastGetActiveDisputesReq == nil {
		t.Fatal("GetActiveDisputes was not forwarded to Librarian")
	}
	if env.librarianSpy.lastGetActiveDisputesReq.GetLawId() != "law-a" {
		t.Fatalf("expected law_id=law-a forwarded, got %q",
			env.librarianSpy.lastGetActiveDisputesReq.GetLawId())
	}

	// Verify response is returned unmodified.
	if len(resp.GetRecords()) != 1 {
		t.Fatalf("expected 1 dispute record, got %d", len(resp.GetRecords()))
	}
	if resp.GetRecords()[0].GetPetitionId() != "pet-1" {
		t.Fatalf("expected petition_id=pet-1, got %q", resp.GetRecords()[0].GetPetitionId())
	}

	// Verify metadata propagation.
	vals := env.librarianSpy.capturedMD.Get("x-flow-workitem-id")
	if len(vals) != 1 || vals[0] != "wi-dispute-test" {
		t.Fatalf("expected metadata propagation, got %v", vals)
	}
}

func TestLibrarianProxy_SearchSimilarLaws_ForwardedToBackend(t *testing.T) {
	env := setupLibrarianProxy(t)

	resp, err := env.proxy.SearchSimilarLaws(context.Background(), &flowv1.SearchSimilarLawsRequest{
		QueryText:   "no agent shall exceed its budget",
		ScopeFilter: "division-a",
		Limit:       5,
	})
	if err != nil {
		t.Fatalf("SearchSimilarLaws: %v", err)
	}

	// Verify request was forwarded to Librarian backend.
	if env.librarianSpy.lastSearchSimilarReq == nil {
		t.Fatal("SearchSimilarLaws was not forwarded to Librarian")
	}
	if env.librarianSpy.lastSearchSimilarReq.GetQueryText() != "no agent shall exceed its budget" {
		t.Fatalf("expected query_text forwarded, got %q",
			env.librarianSpy.lastSearchSimilarReq.GetQueryText())
	}
	if env.librarianSpy.lastSearchSimilarReq.GetScopeFilter() != "division-a" {
		t.Fatalf("expected scope_filter=division-a forwarded, got %q",
			env.librarianSpy.lastSearchSimilarReq.GetScopeFilter())
	}
	if env.librarianSpy.lastSearchSimilarReq.GetLimit() != 5 {
		t.Fatalf("expected limit=5 forwarded, got %d",
			env.librarianSpy.lastSearchSimilarReq.GetLimit())
	}

	// Verify response is returned unmodified.
	if len(resp.GetResults()) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.GetResults()))
	}
	if resp.GetResults()[0].GetLaw().GetId() != "law-similar-1" {
		t.Fatalf("expected law_id=law-similar-1, got %q",
			resp.GetResults()[0].GetLaw().GetId())
	}
	if resp.GetResults()[0].GetSimilarityScore() != 0.92 {
		t.Fatalf("expected similarity_score=0.92, got %f",
			resp.GetResults()[0].GetSimilarityScore())
	}
}

func TestLibrarianProxy_SearchSimilarLaws_PropagatesMetadata(t *testing.T) {
	env := setupLibrarianProxy(t)

	md := metadata.Pairs("x-flow-workitem-id", "wi-search-test")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := env.proxy.SearchSimilarLaws(ctx, &flowv1.SearchSimilarLawsRequest{
		QueryText: "test query",
	})
	if err != nil {
		t.Fatalf("SearchSimilarLaws: %v", err)
	}

	vals := env.librarianSpy.capturedMD.Get("x-flow-workitem-id")
	if len(vals) != 1 || vals[0] != "wi-search-test" {
		t.Fatalf("expected metadata propagation, got %v", vals)
	}
}
