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

// QueueItem represents a HITL queue item returned by the node's REST API.
type QueueItem struct {
	WorkitemID string `json:"workitem_id"`
	ShardID    string `json:"shard_id"`
	QueueName  string `json:"queue_name"`
	Status     string `json:"status"`
	EnqueuedAt string `json:"enqueued_at"`
	ClaimedAt  string `json:"claimed_at"`
}

// Choice represents a single decision option from GET /choices. The wire
// contract is defined once in the shared metadata package and re-exported
// here so the TUI can reference it as api.Choice.
type Choice = flowmeta.Choice

// ChoicesResponse is the optional GET /choices response.
type ChoicesResponse = flowmeta.ChoicesResponse

type ackResponse struct {
	Acknowledged bool `json:"acknowledged"`
}

// HitlError represents a structured error from the node's REST API.
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

// HitlClient communicates with a HITL-capable node's REST API.
type HitlClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewHitlClient creates a client targeting the given base URL
// (typically http://localhost:<localPort>).
func NewHitlClient(baseURL string) *HitlClient {
	return &HitlClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// ProbeQueue sends GET /queue/{workitemID}.
// Returns (QueueItem, nil) on 200, (nil, nil) on 404 (silent non-match),
// or (nil, error) on other responses.
func (c *HitlClient) ProbeQueue(ctx context.Context, workitemID string) (*QueueItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/queue/"+workitemID, nil)
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

// GetChoices sends GET /choices.
// Returns (ChoicesResponse, nil) on 200, (nil, nil) on 404 (fallback to defaults),
// or (nil, error) on other responses.
func (c *HitlClient) GetChoices(ctx context.Context) (*ChoicesResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/choices", nil)
	if err != nil {
		return nil, fmt.Errorf("get choices request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get choices: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var cr ChoicesResponse
		if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
			return nil, fmt.Errorf("decode choices: %w", err)
		}
		return &cr, nil
	case http.StatusNotFound:
		return nil, nil
	default:
		return nil, parseError(resp.Body)
	}
}

// Claim sends POST /queue/{workitemID}/claim.
// Returns (QueueItem, nil) on 200, or (nil, error) on other responses.
func (c *HitlClient) Claim(ctx context.Context, workitemID string) (*QueueItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/queue/"+workitemID+"/claim", nil)
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

// Decide sends POST /queue/{workitemID}/decide with body {"choice":"<value>"}.
// Returns nil on 200 or 202, or error on other responses.
func (c *HitlClient) Decide(ctx context.Context, workitemID, choice string) error {
	body := map[string]string{"choice": choice}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return fmt.Errorf("encode decide body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/queue/"+workitemID+"/decide", &buf)
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

// Release sends POST /queue/{workitemID}/release.
// Returns (QueueItem, nil) on 200, or (nil, error) on other responses.
func (c *HitlClient) Release(ctx context.Context, workitemID string) (*QueueItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/queue/"+workitemID+"/release", nil)
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
