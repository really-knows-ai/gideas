package handlers

import (
	"context"
	"log/slog"
	"os"

	flow "github.com/foundry/flow/sdk/go"
)

// emitCoverageEvent publishes an appraisal.coverage audit event.
// Errors are logged but do not fail the stage.
func emitCoverageEvent(_ context.Context, client *flow.Client, coverage map[string]coverageEntry, cycleID string) {
	units := make([]coverageEntry, 0, len(coverage))
	for _, u := range coverage {
		units = append(units, u)
	}
	payload := map[string]any{
		"stage":    "appraisal",
		"cycle_id": cycleID,
		"units":    units,
	}
	if err := client.PublishAuditEvent(EventAppraisalCoverage, payload,
		os.Getenv(flow.EnvWorkitemID), os.Getenv(flow.EnvFlowNamespace),
	); err != nil {
		slog.Error("appraisal: publish coverage event failed", "error", err)
	} else {
		slog.Info("appraisal: coverage event published")
	}
}

// emitAttestationEvent publishes an appraisal.attestation audit event.
// Errors are logged but do not fail the stage.
func emitAttestationEvent(_ context.Context, client *flow.Client, coverage map[string]coverageEntry, cycleID string) {
	totalViolations := 0
	totalEvals := 0
	completedEvals := 0
	violationsByAppraiser := make(map[string]int)

	for _, u := range coverage {
		totalViolations += u.Violations
		for _, e := range u.Evaluations {
			totalEvals++
			if e.Completed {
				completedEvals++
			}
			violationsByAppraiser[e.Appraiser] += e.Violations
		}
	}

	// Derive status.
	status := "incomplete"
	if completedEvals > 0 && totalViolations == 0 {
		status = "pass"
	} else if completedEvals > 0 && totalViolations > 0 {
		status = "fail"
	}

	appraiserVerdicts := make([]map[string]string, 0, len(violationsByAppraiser))
	for appraiser, violations := range violationsByAppraiser {
		verdict := "resolved"
		if violations > 0 {
			verdict = "violations"
		}
		appraiserVerdicts = append(appraiserVerdicts, map[string]string{
			"appraiser": appraiser,
			"verdict":   verdict,
		})
	}

	payload := map[string]any{
		"stage":              "appraisal",
		"cycle_id":           cycleID,
		"status":             status,
		"violations_total":   totalViolations,
		"appraiser_verdicts": appraiserVerdicts,
	}
	if err := client.PublishAuditEvent(EventAppraisalAttestation, payload,
		os.Getenv(flow.EnvWorkitemID), os.Getenv(flow.EnvFlowNamespace),
	); err != nil {
		slog.Error("appraisal: publish attestation event failed", "error", err)
	} else {
		slog.Info("appraisal: attestation event published", "status", status)
	}
}
