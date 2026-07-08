package api

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// ArtefactInfo represents an artefact returned by ListArtefacts.
type ArtefactInfo struct {
	ID               string
	GovernedArtefact string
}

// FeedbackState mirrors the proto FeedbackState enum, with FEEDBACK_STATE_ prefix stripped.
type FeedbackState int

const (
	FeedbackStateUnspecified FeedbackState = 0
	FeedbackStateNew         FeedbackState = 1
	FeedbackStateActioned    FeedbackState = 2
	FeedbackStateWontFix     FeedbackState = 3
	FeedbackStateRejected    FeedbackState = 4
	FeedbackStateDeadlocked  FeedbackState = 5
	FeedbackStateResolved    FeedbackState = 6
)

// FeedbackItem represents a single feedback entry for an artefact.
type FeedbackItem struct {
	ID         string        // UUID string
	State      FeedbackState
	SourceNode string
	Message    string
	CreatedAt  time.Time
}

// StoreArtefactRequest groups the parameters for StoreArtefact.
type StoreArtefactRequest struct {
	WorkitemID       string
	ArtefactID       string
	GovernedArtefact string
	Content          []byte
	ContentHash      string
}

// ArchivistClient wraps a gRPC connection to the Archivist service.
type ArchivistClient struct {
	Conn *grpc.ClientConn
}

// NewArchivistClient creates a gRPC connection to target (localhost:<port>).
func NewArchivistClient(target string) (*ArchivistClient, error) {
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("archivist grpc dial: %w", err)
	}
	return &ArchivistClient{Conn: conn}, nil
}

// Close closes the gRPC connection.
func (c *ArchivistClient) Close() error {
	if 	c.Conn != nil {
		return 	c.Conn.Close()
	}
	return nil
}

// withMetadata attaches x-flow-namespace and x-flow-workitem-id to the context.
func withMetadata(ctx context.Context, namespace, workitemID string) context.Context {
	md := metadata.Pairs(
		"x-flow-namespace", namespace,
		"x-flow-workitem-id", workitemID,
	)
	return metadata.NewOutgoingContext(ctx, md)
}

// ListArtefacts returns all artefacts for the given workitem.
func (c *ArchivistClient) ListArtefacts(ctx context.Context, namespace, workitemID string) ([]ArtefactInfo, error) {
	ctx = withMetadata(ctx, namespace, workitemID)
	client := flowv1.NewArchivistServiceClient(	c.Conn)
	resp, err := client.ListArtefacts(ctx, &flowv1.ListArtefactsRequest{
		WorkitemId: workitemID,
	})
	if err != nil {
		return nil, fmt.Errorf("list artefacts: %w", err)
	}

	result := make([]ArtefactInfo, 0, len(resp.ArtefactRefs))
	for _, a := range resp.ArtefactRefs {
		result = append(result, ArtefactInfo{
			ID:               a.Id,
			GovernedArtefact: a.GovernedArtefact,
		})
	}

	// Sort by artefact ID lexicographically
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID < result[j].ID
	})

	return result, nil
}

// GetArtefact returns the raw content bytes for a given artefact.
func (c *ArchivistClient) GetArtefact(ctx context.Context, namespace, workitemID, artefactID string) ([]byte, error) {
	ctx = withMetadata(ctx, namespace, workitemID)
	client := flowv1.NewArchivistServiceClient(	c.Conn)
	resp, err := client.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId: workitemID,
		ArtefactId: artefactID,
	})
	if err != nil {
		return nil, fmt.Errorf("get artefact: %w", err)
	}
	return resp.Content, nil
}

// GetFeedback returns all feedback items for a given artefact.
func (c *ArchivistClient) GetFeedback(ctx context.Context, namespace, workitemID, artefactID string) ([]FeedbackItem, error) {
	ctx = withMetadata(ctx, namespace, workitemID)
	client := flowv1.NewArchivistServiceClient(	c.Conn)
	resp, err := client.GetFeedback(ctx, &flowv1.GetFeedbackRequest{
		WorkitemId: workitemID,
		ArtefactId: artefactID,
	})
	if err != nil {
		return nil, fmt.Errorf("get feedback: %w", err)
	}

	result := make([]FeedbackItem, 0, len(resp.FeedbackItems))
	for _, f := range resp.FeedbackItems {
		state := FeedbackState(f.State.Number())
		// Map unknown enum values to Unspecified
		if state < FeedbackStateUnspecified || state > FeedbackStateResolved {
			state = FeedbackStateUnspecified
		}
		result = append(result, FeedbackItem{
			ID:         f.Id,
			State:      state,
			SourceNode: f.Source,
			Message:    f.Message,
			CreatedAt:  f.CreatedAt.AsTime(),
		})
	}

	// Sort by timestamp ascending, stable secondary sort by ID
	sort.SliceStable(result, func(i, j int) bool {
		if !result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].CreatedAt.Before(result[j].CreatedAt)
		}
		return result[i].ID < result[j].ID
	})

	return result, nil
}

// StoreArtefact stores content for a given artefact.
func (c *ArchivistClient) StoreArtefact(ctx context.Context, namespace string, req StoreArtefactRequest) error {
	ctx = withMetadata(ctx, namespace, req.WorkitemID)
	client := flowv1.NewArchivistServiceClient(	c.Conn)
	_, err := client.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       req.WorkitemID,
		ArtefactId:       req.ArtefactID,
		GovernedArtefact: req.GovernedArtefact,
		Content:          req.Content,
		ContentHash:      req.ContentHash,
	})
	if err != nil {
		return fmt.Errorf("store artefact: %w", err)
	}
	return nil
}

// ComputeSHA256 computes the lowercase hex SHA-256 hash of the content.
func ComputeSHA256(content []byte) string {
	h := sha256.Sum256(content)
	return fmt.Sprintf("%x", h[:])
}
