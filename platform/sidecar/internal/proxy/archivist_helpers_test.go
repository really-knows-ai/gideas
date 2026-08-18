package proxy

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/sidecar/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const (
	testContentStr = "test-content"
	childWIStr     = "child-wi"
	parentWIStr    = "parent-wi"
)

// captureArchivistServer captures the StoreArtefact request to verify
// that the Sidecar computed the content hash.
type captureArchivistServer struct {
	flowv1.UnimplementedArchivistServiceServer
	lastStoreReq *flowv1.StoreArtefactRequest
	lastGetReq   *flowv1.GetArtefactRequest
	lastListReq  *flowv1.ListArtefactsRequest
	capturedMD   metadata.MD
}

func (s *captureArchivistServer) StoreArtefact(
	ctx context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.lastStoreReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.StoreArtefactResponse{
		VersionHash:  req.GetContentHash(),
		IsNewVersion: true,
	}, nil
}

func (s *captureArchivistServer) GetArtefact(
	ctx context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	s.lastGetReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.GetArtefactResponse{
		Content:          []byte(testContentStr),
		VersionHash:      "test-hash",
		GovernedArtefact: "txt",
	}, nil
}

func (s *captureArchivistServer) LinkRuling(
	ctx context.Context, req *flowv1.LinkRulingRequest,
) (*flowv1.LinkRulingResponse, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.LinkRulingResponse{
		UpdatedItem: &flowv1.FeedbackItem{
			Id:           req.GetFeedbackId(),
			LinkedRuling: req.GetLawId(),
			State:        flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
		},
	}, nil
}

func (s *captureArchivistServer) ListArtefacts(
	ctx context.Context, req *flowv1.ListArtefactsRequest,
) (*flowv1.ListArtefactsResponse, error) {
	s.lastListReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.ListArtefactsResponse{
		ArtefactRefs: []*flowv1.ArtefactRef{
			{Id: "doc1", GovernedArtefact: "txt"},
		},
	}, nil
}

func setupArchivistProxy(t *testing.T) (*ArchivistProxy, *captureArchivistServer) {
	t.Helper()

	capture := &captureArchivistServer{}
	conn := dialBufconn(t, func(srv *grpc.Server) {
		flowv1.RegisterArchivistServiceServer(srv, capture)
	})

	proxy := &ArchivistProxy{
		client: flowv1.NewArchivistServiceClient(conn),
		conn:   conn,
	}

	return proxy, capture
}

func setupArchivistProxyWithAuth(
	t *testing.T, auth *service.SidecarServer,
) (*ArchivistProxy, *captureArchivistServer) {
	t.Helper()

	capture := &captureArchivistServer{}
	conn := dialBufconn(t, func(srv *grpc.Server) {
		flowv1.RegisterArchivistServiceServer(srv, capture)
	})

	proxy := &ArchivistProxy{
		client:    flowv1.NewArchivistServiceClient(conn),
		conn:      conn,
		childAuth: auth,
	}

	return proxy, capture
}
