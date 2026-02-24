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
	eventType   string
	matchLabels []sqlite.Label
}

// matchesFilter returns true when evt satisfies all non-empty filter
// predicates. An empty predicate matches any value.
//
// Label matching uses AND semantics: every label in matchLabels must
// have at least one matching label on the event (same key and value).
func matchesFilter(evt sqlite.Event, f subscribeFilter) bool {
	if f.eventType != "" && evt.EventType != f.eventType {
		return false
	}
	for _, want := range f.matchLabels {
		if !hasLabel(evt.Labels, want) {
			return false
		}
	}
	return true
}

// hasLabel returns true if labels contains at least one entry with the
// same key and value as want.
func hasLabel(labels []sqlite.Label, want sqlite.Label) bool {
	for _, lbl := range labels {
		if lbl.Key == want.Key && lbl.Value == want.Value {
			return true
		}
	}
	return false
}

// registry manages per-channel subscriber sets. Each subscriber has an
// independent buffered channel so slow consumers never block publishers
// or other subscribers.
type registry struct {
	mu   sync.RWMutex
	subs map[string][]*subscriber // channel -> active subscribers
}

func newRegistry() *registry {
	return &registry{subs: make(map[string][]*subscriber)}
}

const subscriberBufSize = 256

// add creates a new subscriber for the given channel and returns it.
func (r *registry) add(channel string, f subscribeFilter) *subscriber {
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
func (r *registry) remove(channel string, sub *subscriber) {
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
