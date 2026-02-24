package proxy

import (
	"context"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const testLawID = "law-1"

// captureFrictionLedgerServer captures FrictionLedgerService RPC calls.
type captureFrictionLedgerServer struct {
	flowv1.UnimplementedFrictionLedgerServiceServer
	lastQueryReq *flowv1.QueryFrictionRequest
	capturedMD   metadata.MD
	queryResp    *flowv1.QueryFrictionResponse
}

func (s *captureFrictionLedgerServer) QueryFriction(
	ctx context.Context, req *flowv1.QueryFrictionRequest,
) (*flowv1.QueryFrictionResponse, error) {
	s.lastQueryReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	if s.queryResp != nil {
		return s.queryResp, nil
	}
	return &flowv1.QueryFrictionResponse{}, nil
}

type frictionLedgerTestEnv struct {
	proxy  *FrictionLedgerProxy
	ledger *captureFrictionLedgerServer
}

func setupFrictionLedgerProxy(t *testing.T) *frictionLedgerTestEnv {
	t.Helper()

	spy := &captureFrictionLedgerServer{}
	conn := dialBufconn(t, func(s *grpc.Server) {
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

	p := &FrictionLedgerProxy{
		client: flowv1.NewFrictionLedgerServiceClient(conn),
	}

	return &frictionLedgerTestEnv{
		proxy:  p,
		ledger: spy,
	}
}

func TestFrictionLedgerProxy_QueryFriction_ForwardsAndReturns(t *testing.T) {
	env := setupFrictionLedgerProxy(t)

	env.ledger.queryResp = &flowv1.QueryFrictionResponse{
		FrictionAggregates: []*flowv1.FrictionAggregate{
			{LawId: testLawID, TotalMagnitude: 10, EventCount: 3},
		},
	}

	resp, err := env.proxy.QueryFriction(context.Background(), &flowv1.QueryFrictionRequest{
		Filter: &flowv1.FrictionFilter{LawId: testLawID},
	})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}

	if env.ledger.lastQueryReq == nil {
		t.Fatal("QueryFriction was not forwarded")
	}
	if env.ledger.lastQueryReq.GetFilter().GetLawId() != testLawID {
		t.Fatalf("expected filter law_id=%s, got %q", testLawID, env.ledger.lastQueryReq.GetFilter().GetLawId())
	}

	aggs := resp.GetFrictionAggregates()
	if len(aggs) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(aggs))
	}
	if aggs[0].GetLawId() != testLawID || aggs[0].GetTotalMagnitude() != 10 {
		t.Fatalf("unexpected aggregate: %+v", aggs[0])
	}
}

func TestFrictionLedgerProxy_QueryFriction_PropagatesMetadata(t *testing.T) {
	env := setupFrictionLedgerProxy(t)

	md := metadata.Pairs(
		"x-flow-flow-id", "flow-meta",
		"x-flow-workitem-id", "wi-meta",
		"x-flow-node-id", "node-meta",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := env.proxy.QueryFriction(ctx, &flowv1.QueryFrictionRequest{})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}

	assertMD := func(key, expected string) {
		t.Helper()
		vals := env.ledger.capturedMD.Get(key)
		if len(vals) != 1 || vals[0] != expected {
			t.Fatalf("expected %s=%s in forwarded metadata, got %v", key, expected, vals)
		}
	}

	assertMD("x-flow-flow-id", "flow-meta")
	assertMD("x-flow-workitem-id", "wi-meta")
	assertMD("x-flow-node-id", "node-meta")
}
