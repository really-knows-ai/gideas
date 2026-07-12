package rest

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/queue/internal/peer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	perShardTimeout = 5 * time.Second
	overallTimeout  = 30 * time.Second
)

// Handler implements the queue-service REST API and the exported Service interface.
type Handler struct {
	queues []string
	peers  map[string]*peer.PeerClient
}

// NewHandler creates a Handler with the given queue names and peer clients.
func NewHandler(queues []string, peers map[string]*peer.PeerClient) *Handler {
	return &Handler{queues: queues, peers: peers}
}

// ListQueues returns the configured queue names.
func (h *Handler) ListQueues(_ context.Context) ([]string, error) {
	return h.queues, nil
}

// GetQueueItems returns items matching the filter from all shards for the
// given queue name.
func (h *Handler) GetQueueItems(ctx context.Context, queueName string) ([]*flowv1.QueueItem, error) {
	pc, ok := h.peers[queueName]
	if !ok {
		return nil, nil
	}

	shards, err := pc.Peers(ctx)
	if err != nil {
		return nil, err
	}

	type result struct {
		items []*flowv1.QueueItem
		err   error
	}
	results := make(chan result, len(shards))
	var wg sync.WaitGroup

	for _, addr := range shards {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			client, err := pc.GetClient(addr)
			if err != nil {
				results <- result{err: err}
				return
			}
			peerCtx, cancel := context.WithTimeout(ctx, perShardTimeout)
			defer cancel()
			resp, err := client.GetLocalQueue(peerCtx, &flowv1.GetLocalQueueRequest{})
			if err != nil {
				results <- result{err: err}
				return
			}
			results <- result{items: resp.GetItems()}
		}(addr)
	}

	wg.Wait()
	close(results)

	var allItems []*flowv1.QueueItem
	for r := range results {
		if r.err != nil {
			continue
		}
		allItems = append(allItems, r.items...)
	}
	return allItems, nil
}

// RegisterRoutes registers all /queues routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /queues", h.listQueues)
	mux.HandleFunc("GET /queues/{name}", h.getQueueItems)
	mux.HandleFunc("GET /queues/{name}/{id}", h.getQueueItem)
	mux.HandleFunc("POST /queues/{name}/{id}/claim", h.claimItem)
	mux.HandleFunc("POST /queues/{name}/{id}/decide", h.decideItem)
	mux.HandleFunc("POST /queues/{name}/{id}/release", h.releaseItem)
}

func (h *Handler) listQueues(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.queues)
}

func (h *Handler) getQueueItems(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	pc, ok := h.peers[name]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown queue"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), overallTimeout)
	defer cancel()

	shards, err := pc.Peers(ctx)
	if err != nil {
		slog.Warn("queue: dns resolution failed", "queue", name, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":              "dns resolution failed",
			"unreachable_shards": []string{},
		})
		return
	}

	if len(shards) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"items":              []any{},
			"unreachable_shards": []string{},
		})
		return
	}

	type shardResult struct {
		items []*flowv1.QueueItem
		err   error
		addr  string
	}

	results := make(chan shardResult, len(shards))
	var wg sync.WaitGroup

	for _, addr := range shards {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			client, err := pc.GetClient(addr)
			if err != nil {
				slog.Warn("queue: gRPC dial failed", "addr", addr, "queue", name, "error", err)
				results <- shardResult{err: err, addr: addr}
				return
			}

			peerCtx, cancel := context.WithTimeout(ctx, perShardTimeout)
			defer cancel()

			resp, err := client.GetLocalQueue(peerCtx, &flowv1.GetLocalQueueRequest{})
			if err != nil {
				slog.Warn("queue: GetLocalQueue failed", "addr", addr, "queue", name, "error", err)
				results <- shardResult{err: err, addr: addr}
				return
			}
			results <- shardResult{items: resp.GetItems(), addr: addr}
		}(addr)
	}

	wg.Wait()
	close(results)

	var allItems []*flowv1.QueueItem
	unreachable := make([]string, 0)

	for r := range results {
		if r.err != nil {
			unreachable = append(unreachable, r.addr)
			continue
		}
		allItems = append(allItems, r.items...)
	}

	resp := map[string]any{
		"items":              allItems,
		"unreachable_shards": unreachable,
	}

	if len(unreachable) == len(shards) {
		writeJSON(w, http.StatusBadGateway, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) getQueueItem(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	id := r.PathValue("id")

	pc, ok := h.peers[name]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown queue"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), overallTimeout)
	defer cancel()

	shards, err := pc.Peers(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": "dns resolution failed",
		})
		return
	}

	type shardResult struct {
		item *flowv1.QueueItem
		err  error
		addr string
	}
	results := make(chan shardResult, len(shards))
	var wg sync.WaitGroup

	for _, addr := range shards {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			client, err := pc.GetClient(addr)
			if err != nil {
				results <- shardResult{err: err, addr: addr}
				return
			}

			peerCtx, cancel := context.WithTimeout(ctx, perShardTimeout)
			defer cancel()

			resp, err := client.GetLocalQueue(peerCtx, &flowv1.GetLocalQueueRequest{})
			if err != nil {
				results <- shardResult{err: err, addr: addr}
				return
			}
			for _, item := range resp.GetItems() {
				if item.GetWorkitemId() == id {
					results <- shardResult{item: item, addr: addr}
					return
				}
			}
			results <- shardResult{err: status.Error(codes.NotFound, "item not found on shard"), addr: addr}
		}(addr)
	}

	wg.Wait()
	close(results)

	var unreachable []string
	reachableShards := 0
	for r := range results {
		if r.err != nil {
			if st, ok := status.FromError(r.err); ok && st.Code() == codes.NotFound {
				continue
			}
			unreachable = append(unreachable, r.addr)
			continue
		}
		reachableShards++
		if r.item != nil {
			writeJSON(w, http.StatusOK, r.item)
			return
		}
	}

	if len(unreachable) == len(shards) {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":              "all shards unreachable",
			"unreachable_shards": unreachable,
		})
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "item not found"})
}

func (h *Handler) claimItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	call := func(ctx context.Context, c flowv1.QueuePeerServiceClient) (*flowv1.QueueItem, error) {
		resp, err := c.ClaimItem(ctx, &flowv1.ClaimItemRequest{WorkitemId: id})
		if err != nil {
			return nil, err
		}
		return resp.GetItem(), nil
	}
	h.mutateFirstWins(w, r, r.PathValue("name"), call, http.StatusOK, nil)
}

func (h *Handler) decideItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var body struct {
		Choice string `json:"choice"`
	}
	if r.Body != nil && r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
	}

	call := func(ctx context.Context, c flowv1.QueuePeerServiceClient) (*flowv1.QueueItem, error) {
		_, err := c.DecideItem(ctx, &flowv1.DecideItemRequest{
			WorkitemId: id,
			Choice:     body.Choice,
		})
		return nil, err
	}
	h.mutateFirstWins(w, r, r.PathValue("name"), call, http.StatusOK, func(_ *flowv1.QueueItem) any {
		return map[string]any{"acknowledged": true}
	})
}

func (h *Handler) releaseItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	call := func(ctx context.Context, c flowv1.QueuePeerServiceClient) (*flowv1.QueueItem, error) {
		resp, err := c.ReleaseItem(ctx, &flowv1.ReleaseItemRequest{WorkitemId: id})
		if err != nil {
			return nil, err
		}
		return resp.GetItem(), nil
	}
	h.mutateFirstWins(w, r, r.PathValue("name"), call, http.StatusOK, nil)
}

func (h *Handler) mutateFirstWins(
	w http.ResponseWriter, r *http.Request,
	name string,
	call func(context.Context, flowv1.QueuePeerServiceClient) (*flowv1.QueueItem, error),
	successCode int,
	successResp func(item *flowv1.QueueItem) any,
) {
	pc, ok := h.peers[name]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown queue"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), overallTimeout)
	defer cancel()

	shards, err := pc.Peers(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "dns resolution failed",
		})
		return
	}

	if len(shards) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "item not found"})
		return
	}

	type mutateResult struct {
		item *flowv1.QueueItem
		err  error
		addr string
	}
	results := make(chan mutateResult, len(shards))
	var wg sync.WaitGroup

	for _, addr := range shards {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			client, err := pc.GetClient(addr)
			if err != nil {
				results <- mutateResult{err: err, addr: addr}
				return
			}

			peerCtx, cancel := context.WithTimeout(ctx, perShardTimeout)
			defer cancel()

			item, err := call(peerCtx, client)
			results <- mutateResult{item: item, err: err, addr: addr}
		}(addr)
	}

	wg.Wait()
	close(results)

	var unreachable []string
	for r := range results {
		if r.err != nil {
			st, ok := status.FromError(r.err)
			switch {
			case ok && st.Code() == codes.NotFound:
				continue
			case ok && st.Code() == codes.AlreadyExists:
				writeJSON(w, http.StatusConflict, map[string]any{"error": "item already claimed"})
				return
			case ok && st.Code() == codes.FailedPrecondition:
				writeJSON(w, http.StatusConflict, map[string]any{"error": st.Message()})
				return
			default:
				slog.Warn("queue: mutation shard error", "addr", r.addr, "queue", name, "error", r.err)
				unreachable = append(unreachable, r.addr)
			}
			continue
		}
		var resp any = r.item
		if successResp != nil {
			resp = successResp(r.item)
		}
		writeJSON(w, successCode, resp)
		return
	}

	if len(unreachable) == len(shards) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error":              "all shards unreachable",
			"unreachable_shards": unreachable,
		})
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "item not found"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("queue: json encode failed", "error", err)
	}
}
