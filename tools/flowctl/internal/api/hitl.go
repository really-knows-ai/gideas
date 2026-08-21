// Package api provides Kubernetes, Archivist, and HITL client abstractions.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	flowmeta "github.com/foundry/flow/pkg/metadata"
)

// QueueItem represents a HITL queue item returned by the queue-service's REST API.
type QueueItem struct {
	WorkitemID string `json:"workitem_id"`
	ShardID    string `json:"shard_id"`
	QueueName  string `json:"queue_name"`
	Status     string `json:"status"`
	EnqueuedAt string `json:"enqueued_at"`
	ClaimedAt  string `json:"claimed_at"`
	// Choices are the routing options the queue-service serves as item metadata
	// (R-5.2); the node-local /choices route is gone.
	Choices []string `json:"choices"`
}

// ChoicesWithDefault returns q.Choices when non-empty, else the given
// defaults (fallback semantics preserved from the old /choices 404 path).
func (q *QueueItem) ChoicesWithDefault(defaults []string) []string {
	if len(q.Choices) > 0 {
		return q.Choices
	}
	return defaults
}

// Choice is the decision-option shape (value/label/type) the TUI renders,
// sourced from queue item metadata served by the queue-service (no HTTP
// route). The wire contract is defined once in the shared metadata package
// and re-exported here so the TUI can reference it as api.Choice.
type Choice = flowmeta.Choice

type ackResponse struct {
	Acknowledged bool `json:"acknowledged"`
}

// HitlError represents a structured error from the queue-service's REST API.
type HitlError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *HitlError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Known error codes.
const (
	ErrQueueItemNotFound       = "QUEUE_ITEM_NOT_FOUND"
	ErrQueueItemAlreadyClaimed = "QUEUE_ITEM_ALREADY_CLAIMED"
	ErrQueueItemInvalidState   = "QUEUE_ITEM_INVALID_STATE"
	ErrQueueUnavailable        = "QUEUE_UNAVAILABLE"
	ErrBadRequest              = "BAD_REQUEST"
)

// Matching helpers.
func IsQueueItemNotFound(err error) bool { return hasCode(err, ErrQueueItemNotFound) }
func IsAlreadyClaimed(err error) bool    { return hasCode(err, ErrQueueItemAlreadyClaimed) }
func IsInvalidState(err error) bool      { return hasCode(err, ErrQueueItemInvalidState) }
func IsQueueUnavailable(err error) bool  { return hasCode(err, ErrQueueUnavailable) }
func IsBadRequest(err error) bool        { return hasCode(err, ErrBadRequest) }

func hasCode(err error, code string) bool {
	if e, ok := err.(*HitlError); ok {
		return e.Code == code
	}
	return false
}

// HitlClient communicates with the queue-service's REST API.
type HitlClient struct {
	baseURL    string
	queueName  string
	httpClient *http.Client
}

// NewHitlClient creates a client targeting the queue-service at the given
// base URL for the named queue; every path it builds is rooted at
// /queues/{queueName}/.
func NewHitlClient(baseURL, queueName string) *HitlClient {
	return &HitlClient{
		baseURL:    baseURL,
		queueName:  queueName,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// ProbeQueue sends GET /queues/{queueName}/{workitemID}.
// Returns (QueueItem, nil) on 200, (nil, nil) on 404 (silent non-match),
// or (nil, error) on other responses.
func (c *HitlClient) ProbeQueue(ctx context.Context, workitemID string) (*QueueItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/queues/"+c.queueName+"/"+workitemID, nil)
	if err != nil {
		return nil, fmt.Errorf("probe queue request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("probe queue: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var qi QueueItem
		if err := json.NewDecoder(resp.Body).Decode(&qi); err != nil {
			return nil, fmt.Errorf("decode queue item: %w", err)
		}
		return &qi, nil
	case http.StatusNotFound:
		return nil, nil // silent non-match
	default:
		return nil, parseError(resp.Body)
	}
}

// ResolveQueueForItem finds the queue serving workitemID by listing the
// queue-service's queues (GET /queues) and probing each queue's item
// (GET /queues/{name}/{workitemID}) until a match, returning the matching
// item's queue_name. A QUEUE_ITEM_NOT_FOUND error is returned when no queue
// serves it (R-5.3).
func (c *HitlClient) ResolveQueueForItem(ctx context.Context, workitemID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/queues", nil)
	if err != nil {
		return "", fmt.Errorf("list queues request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("list queues: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", parseError(resp.Body)
	}
	// GET /queues returns a bare array of queue names (handleListQueues).
	var queueNames []string
	if err := json.NewDecoder(resp.Body).Decode(&queueNames); err != nil {
		return "", fmt.Errorf("decode queues: %w", err)
	}
	// The receiver's queueName is irrelevant here: per-queue probes are built
	// from the listed names, so a client created before the queue is known
	// (queueName "") resolves correctly.
	for _, name := range queueNames {
		qi, err := NewHitlClient(c.baseURL, name).ProbeQueue(ctx, workitemID)
		if err != nil {
			return "", err
		}
		if qi != nil {
			return qi.QueueName, nil
		}
	}
	return "", &HitlError{Code: ErrQueueItemNotFound, Message: "workitem not found in any queue"}
}

// Claim sends POST /queues/{queueName}/{workitemID}/claim.
// Returns (QueueItem, nil) on 200, or (nil, error) on other responses.
func (c *HitlClient) Claim(ctx context.Context, workitemID string) (*QueueItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/queues/"+c.queueName+"/"+workitemID+"/claim", nil)
	if err != nil {
		return nil, fmt.Errorf("claim request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("claim: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var qi QueueItem
		if err := json.NewDecoder(resp.Body).Decode(&qi); err != nil {
			return nil, fmt.Errorf("decode claimed item: %w", err)
		}
		return &qi, nil
	}
	return nil, parseError(resp.Body)
}

// Decide sends POST /queues/{queueName}/{workitemID}/decide with body
// {"choice":"<value>"}. Returns nil on 200 or 202, or error on other responses.
func (c *HitlClient) Decide(ctx context.Context, workitemID, choice string) error {
	body := map[string]string{"choice": choice}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return fmt.Errorf("encode decide body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/queues/"+c.queueName+"/"+workitemID+"/decide", &buf)
	if err != nil {
		return fmt.Errorf("decide request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("decide: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
		var ack ackResponse
		if err := json.NewDecoder(resp.Body).Decode(&ack); err != nil {
			return fmt.Errorf("decode decide ack: %w", err)
		}
		if !ack.Acknowledged {
			return fmt.Errorf("decide not acknowledged")
		}
		return nil
	}
	return parseError(resp.Body)
}

// Release sends POST /queues/{queueName}/{workitemID}/release.
// Returns (QueueItem, nil) on 200, or (nil, error) on other responses.
func (c *HitlClient) Release(ctx context.Context, workitemID string) (*QueueItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/queues/"+c.queueName+"/"+workitemID+"/release", nil)
	if err != nil {
		return nil, fmt.Errorf("release request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var qi QueueItem
		if err := json.NewDecoder(resp.Body).Decode(&qi); err != nil {
			return nil, fmt.Errorf("decode released item: %w", err)
		}
		return &qi, nil
	}
	return nil, parseError(resp.Body)
}

// errorEnvelope is the JSON error body from the HITL API.
type errorEnvelope struct {
	Error HitlError `json:"error"`
}

// parseError reads a response body and returns either a *HitlError for
// structured JSON errors, or a generic error for non-JSON/malformed bodies.
func parseError(r io.Reader) error {
	var env errorEnvelope
	if err := json.NewDecoder(r).Decode(&env); err != nil {
		// If the body is not JSON or does not match the envelope,
		// read a snippet for a generic error message.
		body, _ := io.ReadAll(r)
		if len(body) > 0 {
			return fmt.Errorf("HITL API error: %s", string(body))
		}
		return fmt.Errorf("HITL API error (malformed response)")
	}
	return &env.Error
}
