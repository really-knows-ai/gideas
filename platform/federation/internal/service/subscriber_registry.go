package service

import (
	"context"
	"slices"
	"sync"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
)

// lawSubscription represents an active SubscribeLawUpdates stream.
type lawSubscription struct {
	flowIdentity string
	stateRefs    []string
	stream       grpc.ServerStreamingServer[flowv1.PublishedLawEvent]
}

// petitionSubscription represents an active SubscribePetitionOutcomes stream.
type petitionSubscription struct {
	flowIdentity string
	stream       grpc.ServerStreamingServer[flowv1.PetitionOutcomeEvent]
}

// SubscriberRegistry is the production EventDispatcher that fans out events
// to active gRPC server streams. It is the bridge between SubmitPublication
// (which calls DispatchLawEvent/DispatchPetitionOutcomeEvent) and the
// SubscribeLawUpdates/SubscribePetitionOutcomes server-streaming RPCs.
//
// Thread-safe: all methods may be called concurrently.
type SubscriberRegistry struct {
	mu                  sync.RWMutex
	lawSubscribers      map[string]*lawSubscription
	petitionSubscribers map[string]*petitionSubscription
}

// NewSubscriberRegistry creates an empty subscriber registry.
func NewSubscriberRegistry() *SubscriberRegistry {
	return &SubscriberRegistry{
		lawSubscribers:      make(map[string]*lawSubscription),
		petitionSubscribers: make(map[string]*petitionSubscription),
	}
}

// RegisterLawSubscriber adds a law-update subscriber. If a subscriber with the
// same flow identity is already registered, it is replaced (last-writer wins).
func (r *SubscriberRegistry) RegisterLawSubscriber(
	flowIdentity string,
	stateRefs []string,
	stream grpc.ServerStreamingServer[flowv1.PublishedLawEvent],
) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lawSubscribers[flowIdentity] = &lawSubscription{
		flowIdentity: flowIdentity,
		stateRefs:    stateRefs,
		stream:       stream,
	}
}

// RemoveLawSubscriber removes a law-update subscriber by flow identity.
func (r *SubscriberRegistry) RemoveLawSubscriber(flowIdentity string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.lawSubscribers, flowIdentity)
}

// HasLawSubscriber reports whether a law-update subscriber is registered for
// the given flow identity. Used for test synchronisation.
func (r *SubscriberRegistry) HasLawSubscriber(flowIdentity string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.lawSubscribers[flowIdentity]
	return ok
}

// RegisterPetitionSubscriber adds a petition-outcome subscriber.
func (r *SubscriberRegistry) RegisterPetitionSubscriber(
	flowIdentity string,
	stream grpc.ServerStreamingServer[flowv1.PetitionOutcomeEvent],
) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.petitionSubscribers[flowIdentity] = &petitionSubscription{
		flowIdentity: flowIdentity,
		stream:       stream,
	}
}

// RemovePetitionSubscriber removes a petition-outcome subscriber.
func (r *SubscriberRegistry) RemovePetitionSubscriber(flowIdentity string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.petitionSubscribers, flowIdentity)
}

// HasPetitionSubscriber reports whether a petition-outcome subscriber is
// registered for the given flow identity.
func (r *SubscriberRegistry) HasPetitionSubscriber(flowIdentity string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.petitionSubscribers[flowIdentity]
	return ok
}

// DispatchLawEvent sends a PublishedLawEvent to all relevant law-update
// subscribers. For state-level publications (Tier 4), only subscribers
// sharing a state with the publisher receive the event. For federation-level
// publications (Tier 5), all subscribers receive the event.
//
// publisherStateRefs identifies which states the publisher belongs to,
// enabling state-level filtering.
//
// Send errors (e.g. from disconnected streams) are silently ignored. The
// subscriber's SubscribeLawUpdates goroutine is responsible for cleanup
// when its context is cancelled.
func (r *SubscriberRegistry) DispatchLawEvent(
	_ context.Context,
	event *flowv1.PublishedLawEvent,
	publisherStateRefs []string,
) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Determine if this is a federation-level publication (all subscribers)
	// or state-level (only subscribers in the publisher's states).
	isFederationLevel := event.GetMaterialisationTier() == flowv1.LawTier_LAW_TIER_FEDERAL_ACCORD

	for _, sub := range r.lawSubscribers {
		// Skip the publisher itself (don't echo back).
		if sub.flowIdentity == event.GetPublisherFlowIdentity() {
			continue
		}

		if !isFederationLevel {
			// State-level: subscriber must share at least one state with the publisher.
			if !sharesAnyState(sub.stateRefs, publisherStateRefs) {
				continue
			}
		}

		//nolint:errcheck // Best-effort delivery; disconnected streams handled by context cancellation.
		_ = sub.stream.Send(event)
	}
}

// DispatchPetitionOutcomeEvent sends a PetitionOutcomeEvent to all
// petition-outcome subscribers. Petition outcomes are broadcast to all
// subscribers (the subscribing flow filters relevant petition IDs locally).
func (r *SubscriberRegistry) DispatchPetitionOutcomeEvent(_ context.Context, event *flowv1.PetitionOutcomeEvent) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, sub := range r.petitionSubscribers {
		//nolint:errcheck // Best-effort delivery.
		_ = sub.stream.Send(event)
	}
}

// sharesAnyState reports whether a and b share at least one common state ref.
func sharesAnyState(a, b []string) bool {
	for _, sa := range a {
		if slices.Contains(b, sa) {
			return true
		}
	}
	return false
}
