package flow

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// apiError is the standard error response envelope for the HITL REST API.
// Error codes match the stable codes from specs/05-reference/error-catalogue.md.
type apiError struct {
	Error apiErrorDetail `json:"error"`
}

type apiErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// newHITLRouter returns an http.Handler with the HITL REST API routes.
// Uses Go 1.22+ enhanced routing with method+pattern matching.
func newHITLRouter(qm QueueManager) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /queue", handleListQueue(qm))
	mux.HandleFunc("GET /queue/{id}", handleGetItem(qm))
	mux.HandleFunc("POST /queue/{id}/claim", handleClaim(qm))
	mux.HandleFunc("POST /queue/{id}/decide", handleDecide(qm))
	mux.HandleFunc("POST /queue/{id}/release", handleRelease(qm))
	return mux
}

// handleListQueue returns all queue items via scatter-gather.
// Query params: status, limit, offset.
func handleListQueue(qm QueueManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter := QueueFilter{}

		if s := r.URL.Query().Get("status"); s != "" {
			st := QueueStatus(s)
			filter.Status = &st
		}
		if l := r.URL.Query().Get("limit"); l != "" {
			if v, err := strconv.Atoi(l); err == nil {
				filter.Limit = v
			}
		}
		if o := r.URL.Query().Get("offset"); o != "" {
			if v, err := strconv.Atoi(o); err == nil {
				filter.Offset = v
			}
		}

		items, err := qm.GetGlobalQueue(r.Context(), filter)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}

		// Return empty array instead of null.
		if items == nil {
			items = []QueueItem{}
		}
		writeJSON(w, http.StatusOK, items)
	}
}

// handleGetItem returns a single queue item by Workitem ID.
func handleGetItem(qm QueueManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		item, err := qm.GetItem(r.Context(), id)
		if err != nil {
			writeQueueError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

// handleClaim transitions an item from "waiting" to "claimed".
func handleClaim(qm QueueManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		item, err := qm.Claim(r.Context(), id)
		if err != nil {
			writeQueueError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

// handleDecide signals that a decision has been made. The item is deleted
// from the queue. No request body is required — the decision itself is
// expressed through normal SDK operations.
func handleDecide(qm QueueManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		err := qm.Complete(r.Context(), id)
		if err != nil {
			writeQueueError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"acknowledged": true})
	}
}

// handleRelease transitions a "claimed" item back to "waiting".
func handleRelease(qm QueueManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		item, err := qm.Release(r.Context(), id)
		if err != nil {
			writeQueueError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

// writeQueueError maps sentinel errors to HTTP status + error code.
func writeQueueError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrQueueItemNotFound):
		writeAPIError(w, http.StatusNotFound, "QUEUE_ITEM_NOT_FOUND", err.Error())
	case errors.Is(err, ErrQueueItemAlreadyClaimed):
		writeAPIError(w, http.StatusConflict, "QUEUE_ITEM_ALREADY_CLAIMED", err.Error())
	case errors.Is(err, ErrQueueItemInvalidState):
		writeAPIError(w, http.StatusConflict, "QUEUE_ITEM_INVALID_STATE", err.Error())
	case errors.Is(err, ErrShardUnavailable):
		writeAPIError(w, http.StatusServiceUnavailable, "QUEUE_UNAVAILABLE", err.Error())
	default:
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
}

// writeAPIError writes a structured error response.
func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiError{
		Error: apiErrorDetail{Code: code, Message: message},
	})
}

// writeJSON writes a JSON response with the given status code.
//
//nolint:unparam // status is always 200 today but the API may grow.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
