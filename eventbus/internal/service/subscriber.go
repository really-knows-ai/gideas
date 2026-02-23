// Package service implements the FlowEventBusService gRPC server.
package service

import (
	"sync"

	"github.com/gideas/flow/eventbus/internal/store/sqlite"
)

// subscriber represents a single active subscription on a channel.
type subscriber struct {
	ch     chan sqlite.Event
	filter subscribeFilter
}

// subscribeFilter holds the optional narrowing predicates for a
// subscriber. Zero-value fields match everything.
type subscribeFilter struct {
	eventType string
	lawID     string
}

// matchesFilter returns true when evt satisfies all non-empty filter
// predicates. An empty predicate matches any value.
func matchesFilter(evt sqlite.Event, f subscribeFilter) bool {
	if f.eventType != "" && evt.EventType != f.eventType {
		return false
	}
	if f.lawID != "" {
		lawIDs, ok := evt.Attributes["law_ids"]
		if !ok {
			return false
		}
		if !containsElement(lawIDs, f.lawID) {
			return false
		}
	}
	return true
}

// containsElement checks whether needle appears as an exact
// comma-separated element in csv.
func containsElement(csv, needle string) bool {
	start := 0
	for i := 0; i <= len(csv); i++ {
		if i == len(csv) || csv[i] == ',' {
			if csv[start:i] == needle {
				return true
			}
			start = i + 1
		}
	}
	return false
}

// registry manages per-channel subscriber sets. Each subscriber has an
// independent buffered channel so slow consumers never block publishers
// or other subscribers.
type registry struct {
	mu   sync.RWMutex
	subs map[int32][]*subscriber // channel -> active subscribers
}

func newRegistry() *registry {
	return &registry{subs: make(map[int32][]*subscriber)}
}

const subscriberBufSize = 256

// add creates a new subscriber for the given channel and returns it.
func (r *registry) add(channel int32, f subscribeFilter) *subscriber {
	sub := &subscriber{
		ch:     make(chan sqlite.Event, subscriberBufSize),
		filter: f,
	}
	r.mu.Lock()
	r.subs[channel] = append(r.subs[channel], sub)
	r.mu.Unlock()
	return sub
}

// remove unregisters a subscriber and closes its channel.
func (r *registry) remove(channel int32, sub *subscriber) {
	r.mu.Lock()
	defer r.mu.Unlock()
	subs := r.subs[channel]
	for i, s := range subs {
		if s == sub {
			r.subs[channel] = append(subs[:i], subs[i+1:]...)
			close(sub.ch)
			return
		}
	}
}

// fanOut delivers evt to all active subscribers on the event's channel.
// Slow subscribers whose buffer is full are skipped (non-blocking send).
func (r *registry) fanOut(evt sqlite.Event) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, sub := range r.subs[evt.Channel] {
		if !matchesFilter(evt, sub.filter) {
			continue
		}
		// Non-blocking send — slow subscribers are isolated.
		select {
		case sub.ch <- evt:
		default:
		}
	}
}
