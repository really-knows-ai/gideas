# Foundry Flow: Metrics Catalog

> **Version:** 1.0.0  
> **Status:** Implementation Contract

This document defines the canonical metrics for Foundry Flow observability.

---

## 1. Observability Philosophy

### The Three Pillars

| Pillar | Purpose | Implementation |
|--------|---------|----------------|
| **Metrics** | Quantitative health signals | Prometheus endpoints on each component |
| **Logs** | Event-level debugging | Structured JSON to stdout (K8s native) |
| **Traces** | Request flow visualization | OpenTelemetry spans (optional) |

### Design Principles

1. **Metrics are Cheap, Logs are Expensive:** Prefer counters/gauges over log parsing for alerting.
2. **Business Metrics > Infrastructure Metrics:** Track "Workitems completed" before "CPU usage."
3. **Alert on Symptoms, Not Causes:** Alert when throughput drops, investigate CPU later.
4. **Golden Signals First:** Latency, Traffic, Errors, Saturation (RED/USE).

---

## 2. Prometheus Endpoint Strategy

| Source | Port | Role |
|--------|------|------|
| **Flow Monitor** | `:9090/metrics` | Telemetry, friction, audit logging |
| **Flow Operator** | `:8080/metrics` | Orchestration health |
| **Sidecars** | `:35699/metrics` | Infrastructure health |

---

## 3. Domain A: The Pulse (Workitem Lifecycle)

*Tracks the movement of work through the system. The primary throughput and latency signals.*

| Metric | Type | Labels | Source | Description |
|--------|------|--------|--------|-------------|
| `foundry_workitem_transition_total` | Counter | `flow`, `state`, `terminal_contract` | Operator | **Golden Signal.** Counts state transitions. |
| `foundry_workitem_duration_seconds` | Histogram | `flow`, `terminal_contract` | Operator | End-to-end latency from creation to terminal state. |
| `foundry_workitem_active_count` | Gauge | `flow`, `state` | Operator | Current queue depth (pending, running). |
| `foundry_workitem_failure_total` | Counter | `flow`, `reason`, `last_node` | Operator | Failures by reason (timeout, thrash, signature_invalid). |
| `foundry_workitem_thrash_total` | Counter | `flow`, `node` | Sidecar | Thrash Guard triggers. |

**Key Queries:**
```promql
# Throughput (workitems/minute)
rate(foundry_workitem_transition_total{state="completed"}[1m]) * 60

# Error rate
rate(foundry_workitem_failure_total[5m]) / rate(foundry_workitem_transition_total[5m])

# P99 latency
histogram_quantile(0.99, rate(foundry_workitem_duration_seconds_bucket[5m]))
```

---

## 4. Domain B: The Nervous System (Friction & Cost)

*Tracks the "pain" and expense of the flow. Quality and financial signals.*

| Metric | Type | Labels | Source | Description |
|--------|------|--------|--------|-------------|
| `foundry_friction_score_total` | Counter | `flow`, `node`, `law_id`, `attribution` | Flow Monitor | Quality signal. |
| `foundry_cost_llm_total` | Counter | `flow`, `node`, `model`, `phase` | Flow Monitor | Financial tracking. |
| `foundry_cost_llm_tokens_total` | Counter | `flow`, `node`, `model`, `direction` | Flow Monitor | Token usage. |
| `foundry_system_friction_total` | Counter | `flow`, `source` | Flow Monitor | Governance overhead. |

**Labels:**
| Label | Values | Purpose |
|-------|--------|---------|
| `attribution` | `law_violation`, `feedback_loop`, `timeout` | Why friction was incurred |
| `phase` | `generation`, `review`, `assay` | Pipeline stage |
| `direction` | `input`, `output` | Token direction |

**Key Queries:**
```promql
# LLM cost per hour
increase(foundry_cost_llm_total[1h])

# Friction by law (find problematic rules)
topk(10, sum by (law_id) (rate(foundry_friction_score_total[1h])))

# Cost per completed workitem
sum(increase(foundry_cost_llm_total[1h])) / sum(increase(foundry_workitem_transition_total{state="completed"}[1h]))
```

---

## 5. Domain C: The Supply Chain (Node Health)

*Tracks the behavior and capacity of worker pods.*

| Metric | Type | Labels | Source | Description |
|--------|------|--------|--------|-------------|
| `foundry_node_boot_duration_seconds` | Histogram | `node`, `image_digest` | Sidecar | Cold start latency. |
| `foundry_node_handler_duration_seconds` | Histogram | `node`, `handler` | Sidecar | Execution time of user logic. |
| `foundry_node_ready_status` | Gauge | `node`, `reason` | Sidecar | Real-time capacity. |
| `foundry_node_heartbeat_miss_total` | Counter | `node` | Sidecar | Early warning for zombies. |
| `foundry_node_concurrent_workitems` | Gauge | `node` | Sidecar | Current concurrency utilization. |

**Node Ready Status Values:** `booting`, `idle`, `busy`, `draining`

**Key Queries:**
```promql
# Slow nodes (P95 handler time > 30s)
histogram_quantile(0.95, rate(foundry_node_handler_duration_seconds_bucket[5m])) > 30

# Capacity utilization
sum(foundry_node_concurrent_workitems) / sum(foundry_node_concurrency_limit)
```

---

## 6. Domain D: The Bureaucracy (Governance & Law)

*Tracks the legal system's activity and health.*

| Metric | Type | Labels | Source | Description |
|--------|------|--------|--------|-------------|
| `foundry_legal_citations_total` | Counter | `law_id`, `node`, `tier` | Librarian | Law usage heatmap. |
| `foundry_legal_findings_total` | Counter | `status` | Librarian | New findings (pending, ready, failed, duplicate). |
| `foundry_legal_conflicts_total` | Counter | `severity` | Librarian | Governance failures. |
| `foundry_assay_verdict_total` | Counter | `outcome`, `ruling_id` | Assay Node | Judicial outcomes. |
| `foundry_assay_rounds_total` | Counter | `ruling_id` | Assay Node | Deliberation rounds. |
| `foundry_hitl_queue_depth` | Gauge | `node`, `status` | HITL Node | Human queue depth. |
| `foundry_hitl_decision_duration_seconds` | Histogram | `node` | HITL Node | Human response time. |

**Key Queries:**
```promql
# Most cited laws (candidates for promotion)
topk(10, sum by (law_id) (increase(foundry_legal_citations_total[24h])))

# Hung jury rate
rate(foundry_assay_verdict_total{outcome="hung_jury"}[1h]) / rate(foundry_assay_verdict_total[1h])

# HITL SLA (% decided within 4 hours)
histogram_quantile(0.9, rate(foundry_hitl_decision_duration_seconds_bucket[24h])) < 14400
```

---

## 7. Domain E: The Machinery (System Infrastructure)

*Tracks platform stability and security.*

| Metric | Type | Labels | Source | Description |
|--------|------|--------|--------|-------------|
| `foundry_pki_cert_expiry_seconds` | Gauge | `subject_cn`, `issuer` | Operator | **Critical.** Days to doom. |
| `foundry_librarian_integrity_check_total` | Counter | `status` | Librarian | DB corruption detection. |
| `foundry_librarian_sequence_id` | Gauge | | Librarian | Current WAL sequence. |
| `foundry_lawsearch_sequence_id` | Gauge | `replica` | Law Search | Replica's current sequence (for freshness SLO). |
| `foundry_monitor_buffer_size_bytes` | Gauge | `router_id` | Router | Backpressure indicator. |
| `foundry_monitor_events_total` | Counter | `type`, `status` | Router | Event throughput. |
| `foundry_grpc_server_errors_total` | Counter | `service`, `code` | All | Transport health. |
| `foundry_embedding_requests_total` | Counter | `status`, `tier` | Librarian | Embedding provider health. |
| `foundry_embedding_duration_seconds` | Histogram | `tier` | Librarian | Embedding latency. |

**Key Queries:**
```promql
# Days until certificate expires
foundry_pki_cert_expiry_seconds / 86400

# Embedding provider error rate
rate(foundry_embedding_requests_total{status="error"}[5m]) / rate(foundry_embedding_requests_total[5m])

# Telemetry backlog growth
deriv(foundry_monitor_buffer_size_bytes[5m])
```

---

## 8. Metric Exposure Requirements

| Component | Port | Required Metrics | Optional Metrics |
|-----------|------|------------------|------------------|
| **Flow Monitor** | 9090 | `foundry_friction_*`, `foundry_cost_*`, `foundry_monitor_*` | `foundry_system_friction_*` |
| **Flow Operator** | 8080 | `foundry_workitem_*`, `foundry_pki_*` | `workqueue_*` (controller-runtime) |
| **Sidecar** | 35699 | `foundry_node_*` | `process_*`, `go_*` (runtime) |
| **Librarian** | 8080 | `foundry_legal_*`, `foundry_librarian_*`, `foundry_embedding_*` | |
| **Law Search** | 8080 | `foundry_lawsearch_*` | |
| **HITL Node** | 8080 | `foundry_hitl_*` | |
| **Assay Node** | 8080 | `foundry_assay_*` | |
| **Flow Monitor** | 8080 | `foundry_monitor_*` | |

---

## 9. Cardinality Guidelines

### Safe Labels (Low Cardinality)

| Label | Max Values | Safe? |
|-------|------------|-------|
| `flow` | ~10 per cluster | ✅ |
| `state` | 5 | ✅ |
| `tier` | 4 | ✅ |
| `severity` | 4 | ✅ |
| `node` | ~50 per flow | ✅ |
| `model` | ~10 | ✅ |

### Dangerous Labels (High Cardinality)

| Label | Risk | Mitigation |
|-------|------|------------|
| `workitem_id` | ❌ Unbounded | Never use in metrics. Use logs/traces. |
| `law_id` | ⚠️ Thousands | Aggregate in recording rules. Limit to top-k queries. |
| `user_id` | ❌ Unbounded | Track in application logs, not Prometheus. |

### Recording Rules for Aggregation

```yaml
- record: foundry:friction_by_law_1h
  expr: sum by (law_id) (increase(foundry_friction_score_total[1h]))

- record: foundry:top_laws_by_friction
  expr: topk(100, foundry:friction_by_law_1h)
```
