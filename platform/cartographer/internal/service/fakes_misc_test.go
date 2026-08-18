package service

import (
	"context"
	"log/slog"
	"sync"

	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/metadata"
)

type testSchemaProvider struct {
	entityNames []string
	edgeNames   []string
	entities    map[string]*store.EntityTypeDef
	edges       map[string]*store.EdgeTypeDef
}

func (s testSchemaProvider) EntityTypeNames() []string {
	return append([]string(nil), s.entityNames...)
}
func (s testSchemaProvider) EdgeTypeNames() []string { return append([]string(nil), s.edgeNames...) }
func (s testSchemaProvider) EntityType(name string) (*store.EntityTypeDef, bool) {
	def, ok := s.entities[name]
	return def, ok
}
func (s testSchemaProvider) EdgeType(name string) (*store.EdgeTypeDef, bool) {
	def, ok := s.edges[name]
	return def, ok
}

type mockTelemetryPublisher struct {
	mu     sync.Mutex
	events []*flowv1.PublishRequest
}

func (m *mockTelemetryPublisher) Submit(req *flowv1.PublishRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, req)
}
func (m *mockTelemetryPublisher) Events() []*flowv1.PublishRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := make([]*flowv1.PublishRequest, len(m.events))
	copy(r, m.events)
	return r
}

// mockExportStream implements grpc.ServerStreamingServer[flowv1.ExportGraphResponse]
// for testing ExportGraph through the stream interceptor.
type mockExportStream struct {
	ctx          context.Context
	sendErr      error
	sendCount    int
	data         []byte
	verifiedCaps *Capabilities
}

func (m *mockExportStream) Send(resp *flowv1.ExportGraphResponse) error {
	return m.SendMsg(resp)
}
func (m *mockExportStream) SendMsg(msg any) error {
	m.sendCount++
	if resp, ok := msg.(*flowv1.ExportGraphResponse); ok {
		m.data = append(m.data, resp.Chunk...)
	}
	return m.sendErr
}
func (m *mockExportStream) Context() context.Context        { return m.ctx }
func (m *mockExportStream) SetTrailer(md metadata.MD)       {}
func (m *mockExportStream) SendHeader(md metadata.MD) error { return nil }
func (m *mockExportStream) SetHeader(md metadata.MD) error  { return nil }
func (m *mockExportStream) RecvMsg(any) error               { return nil }

// captureLogHandler collects slog records so a test can assert on recovery
// log output.
type captureLogHandler struct {
	mu       sync.Mutex
	messages []string
}

func (h *captureLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureLogHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = append(h.messages, r.Message)
	return nil
}
func (h *captureLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureLogHandler) WithGroup(string) slog.Handler      { return h }
