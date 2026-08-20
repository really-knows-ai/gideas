package service

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// RestServer is the queue-service's REST frontend (R-B4). Routes use the plural
// /queues — distinct from the SDK's /queue mesh-local surface. Every per-item
// op proxies to the living owning shard via QueuePeerService (peerProxy).
type RestServer struct {
	reg *Registry
}

func NewRestServer(reg *Registry) *RestServer {
	return &RestServer{reg: reg}
}

func (s *RestServer) Handler() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /queues", s.handleListQueues)
	mux.HandleFunc("GET /queues/{name}", s.handleListItems)
	mux.HandleFunc("GET /queues/{name}/{id}", s.handleGetItem)
	mux.HandleFunc("POST /queues/{name}/{id}/claim", s.handleClaim)
	mux.HandleFunc("POST /queues/{name}/{id}/decide", s.handleDecide)
	mux.HandleFunc("POST /queues/{name}/{id}/release", s.handleRelease)
	return mux
}

// handleListQueues lists registered queue names from the CR registry.
func (s *RestServer) handleListQueues(w http.ResponseWriter, r *http.Request) {
	resp, err := s.reg.ListQueues(r.Context(), &flowv1.ListQueuesRequest{})
	if err != nil {
		writeRestError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	names := make([]string, 0, len(resp.GetQueues()))
	for _, q := range resp.GetQueues() {
		names = append(names, q.GetQueueName())
	}
	writeRestJSON(w, names)
}

// handleListItems lists items for one queue: raw GetLocalQueue scatter-gather
// across every living shard (no per-workitem_id dedupe — PHASE_04 applies it).
func (s *RestServer) handleListItems(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	proxy := newPeerProxy(s.reg)
	defer proxy.close()
	items, err := proxy.listItems(r.Context(), name)
	if err != nil {
		writeRestProxyError(w, err)
		return
	}
	if items == nil {
		items = []*flowv1.QueueItem{}
	}
	writeRestJSON(w, items)
}

// handleGetItem returns a single queue item by workitem id proxied to the
// living owning shard.
func (s *RestServer) handleGetItem(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	id := r.PathValue("id")
	proxy := newPeerProxy(s.reg)
	defer proxy.close()
	item, err := proxy.getItem(r.Context(), name, id)
	if err != nil {
		writeRestProxyError(w, err)
		return
	}
	writeRestJSON(w, item)
}

// handleClaim proxies ClaimItem to the living owning shard.
func (s *RestServer) handleClaim(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	id := r.PathValue("id")
	proxy := newPeerProxy(s.reg)
	defer proxy.close()
	item, err := proxy.claim(r.Context(), name, id)
	if err != nil {
		writeRestProxyError(w, err)
		return
	}
	writeRestJSON(w, item)
}

// handleDecide proxies DecideItem (with optional choice body) to the living
// owner.
func (s *RestServer) handleDecide(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	id := r.PathValue("id")

	var choice string
	if r.ContentLength != 0 {
		var body struct {
			Choice string `json:"choice"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeRestError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON body: "+err.Error())
			return
		}
		choice = body.Choice
	}

	proxy := newPeerProxy(s.reg)
	defer proxy.close()
	if err := proxy.decide(r.Context(), name, id, choice); err != nil {
		writeRestProxyError(w, err)
		return
	}
	writeRestJSON(w, map[string]bool{"acknowledged": true})
}

// handleRelease proxies ReleaseItem to the living owning shard.
func (s *RestServer) handleRelease(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	id := r.PathValue("id")
	proxy := newPeerProxy(s.reg)
	defer proxy.close()
	item, err := proxy.release(r.Context(), name, id)
	if err != nil {
		writeRestProxyError(w, err)
		return
	}
	writeRestJSON(w, item)
}

// writeRestProxyError maps proxy sentinel errors to the REST envelope:
// QUEUE_ITEM_NOT_FOUND→404, QUEUE_ITEM_ALREADY_CLAIMED/INVALID_STATE→409,
// QUEUE_UNAVAILABLE→503.
func writeRestProxyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errQueueItemNotFound):
		writeRestError(w, http.StatusNotFound, "QUEUE_ITEM_NOT_FOUND", err.Error())
	case errors.Is(err, errQueueItemAlreadyClaimed):
		writeRestError(w, http.StatusConflict, "QUEUE_ITEM_ALREADY_CLAIMED", err.Error())
	case errors.Is(err, errQueueItemInvalidState):
		writeRestError(w, http.StatusConflict, "QUEUE_ITEM_INVALID_STATE", err.Error())
	case errors.Is(err, errShardUnavailable):
		writeRestError(w, http.StatusServiceUnavailable, "QUEUE_UNAVAILABLE", err.Error())
	default:
		slog.Warn("queue-service: rest proxy error", "error", err)
		writeRestError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
}

type restError struct {
	Error restErrorDetail `json:"error"`
}

type restErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeRestError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(restError{Error: restErrorDetail{Code: code, Message: message}})
}

func writeRestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}
