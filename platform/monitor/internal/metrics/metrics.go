// Package metrics defines Prometheus metric definitions for the Flow Monitor.
//
// The Flow Monitor is a stateless pipeline adapter that subscribes to the
// Event Bus and exports telemetry as Prometheus metrics and audit events as
// JSON Lines to stdout.
//
// See: specs/02-flow/04-system-services.md (Service Invariant #16)
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Friction metrics — counters for friction magnitude by law/node.
var (
	// FrictionTotal counts the total accumulated friction magnitude,
	// labelled by law_id and node_id.
	FrictionTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "foundry",
		Subsystem: "friction",
		Name:      "total",
		Help:      "Total accumulated friction magnitude by law and node.",
	}, []string{"law_id", "node_id"})

	// FrictionEvents counts the number of friction events, labelled by
	// law_id and node_id.
	FrictionEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "foundry",
		Subsystem: "friction",
		Name:      "events_total",
		Help:      "Total number of friction events by law and node.",
	}, []string{"law_id", "node_id"})

	// ThresholdCrossings counts the number of threshold-crossing events,
	// labelled by law_id and tier.
	ThresholdCrossings = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "foundry",
		Subsystem: "friction",
		Name:      "threshold_crossings_total",
		Help:      "Total number of friction threshold-crossing events by law and tier.",
	}, []string{"law_id", "tier"})
)

// Telemetry metrics — generic counters for custom telemetry events.
var (
	// TelemetryEvents counts telemetry events by event_type and node_id.
	TelemetryEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "foundry",
		Subsystem: "telemetry",
		Name:      "events_total",
		Help:      "Total number of telemetry events by event type and node.",
	}, []string{"event_type", "node_id"})
)

// Audit metrics — counters for audit events.
var (
	// AuditEvents counts audit events by event_type.
	AuditEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "foundry",
		Subsystem: "audit",
		Name:      "events_total",
		Help:      "Total number of audit events by event type.",
	}, []string{"event_type"})
)

// Subscriber health metrics.
var (
	// SubscriberErrors counts subscription errors by channel.
	SubscriberErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "foundry",
		Subsystem: "monitor",
		Name:      "subscriber_errors_total",
		Help:      "Total number of Event Bus subscription errors by channel.",
	}, []string{"channel"})
)
