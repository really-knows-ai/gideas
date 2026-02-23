package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestMetricsRegistered(t *testing.T) {
	// Verify that all promauto-registered metrics are present in the
	// default registry. Gathering should succeed without errors.
	_, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	// promauto registers lazily. Trigger at least one label combination
	// so the metrics appear.
	FrictionTotal.WithLabelValues("law-test", "node-test").Add(1)
	FrictionEvents.WithLabelValues("law-test", "node-test").Inc()
	ThresholdCrossings.WithLabelValues("law-test", "1").Inc()
	TelemetryEvents.WithLabelValues("friction", "node-test").Inc()
	AuditEvents.WithLabelValues("audit.test").Inc()
	SubscriberErrors.WithLabelValues("telemetry").Inc()

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("Gather after increment: %v", err)
	}

	expected := map[string]bool{
		"foundry_friction_total":                     false,
		"foundry_friction_events_total":              false,
		"foundry_friction_threshold_crossings_total": false,
		"foundry_telemetry_events_total":             false,
		"foundry_audit_events_total":                 false,
		"foundry_monitor_subscriber_errors_total":    false,
	}

	for _, f := range families {
		if _, ok := expected[f.GetName()]; ok {
			expected[f.GetName()] = true
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("metric %q not found in default registry", name)
		}
	}
}
