# Foundry Flow: Alerting & Operations

> **Version:** 1.0.0  
> **Status:** Implementation Contract

This document defines alerting baselines, dashboard recommendations, and operational playbooks.

---

## 1. Alerting Baselines

### 1.1 Severity Levels

| Severity | Response Time | Notification | Examples |
|----------|---------------|--------------|----------|
| **CRITICAL** | Immediate (page) | PagerDuty/OpsGenie | Gridlock, DB corruption, cert expiry <3d |
| **ERROR** | Within 1 hour | Slack #alerts | High failure rate, embedding provider down |
| **WARNING** | Business hours | Slack #warnings | Friction spike, zombie node, queue backlog |
| **INFO** | Review weekly | Dashboard only | Anomaly detection, trend changes |

### 1.2 Critical Alerts

| Alert | Condition | For | Description |
|-------|-----------|-----|-------------|
| **GridlockDetected** | `rate(transitions[5m]) == 0 AND active_count > 0` | 5m | Flow is stuck. |
| **BrainLobotomy** | `integrity_check{status="fail"} > 0` | 1m | Librarian DB corrupt. |
| **CertExpiryImminent** | `cert_expiry_seconds < 259200` | 1m | mTLS chain breaks in <3 days. |

### 1.3 Error Alerts

| Alert | Condition | For | Description |
|-------|-----------|-----|-------------|
| **EmbeddingProviderDown** | `failures > 0 AND successes == 0` | 10m | No successful embeddings. |
| **WorkitemFailureSpike** | `failure_rate > 10%` | 5m | High error rate in flow. |
| **TelemetryBackpressure** | `buffer_size > 100MB` | 5m | Downstream consumers can't keep up. |

### 1.4 Warning Alerts

| Alert | Condition | For | Description |
|-------|-----------|-----|-------------|
| **FrictionSpike** | `current > 3 * hourly_avg` | 10m | Anomalous resistance. |
| **ZombieNode** | `heartbeat_misses > 5 in 2m` | 2m | Node alive but silent. |
| **HungJury** | `hung_jury failures > 0` | 1m | Assay couldn't decide. |
| **HITLQueueBacklog** | `queue_depth > 50` | 30m | Human review backing up. |
| **CertExpiryWarning** | `cert_expiry_seconds < 604800` | 1m | Certificate expires in <7 days. |
| **WorkitemThrashing** | `thrash_total > 0` | 1m | Routing loop detected. |

---

## 2. Dashboard Recommendations

### 2.1 Executive Dashboard (Business Health)

**Panels:**
1. **Throughput:** Workitems completed per hour (line chart)
2. **Error Rate:** Failure percentage (gauge, red >5%)
3. **P99 Latency:** End-to-end time (line chart)
4. **LLM Cost:** Cumulative spend (counter)
5. **Active Work:** Current queue depth (gauge)

### 2.2 Operations Dashboard (System Health)

**Panels:**
1. **Certificate Expiry:** Days remaining per cert (table)
2. **Node Readiness:** Pods by status (pie chart)
3. **Telemetry Buffer:** Buffer size over time (line chart)
4. **Embedding Health:** Success/failure rate (stacked bar)
5. **Librarian Sequence:** WAL progression (monotonic line)

### 2.3 Governance Dashboard (Law Library Health)

**Panels:**
1. **Top Cited Laws:** Heatmap of citations (table)
2. **Friction by Law:** Friction attributed to each law (bar chart)
3. **Assay Outcomes:** Verdict distribution (pie chart)
4. **HITL Queue:** Depth over time by node (line chart)
5. **Law Lifecycle:** Findings created/expired/promoted (stacked area)

---

## 3. SLO Recommendations

### 3.1 Availability SLO

**Target:** 99.9% of workitems complete successfully within 1 hour.

```promql
# Error budget burn rate
(
  1 - (
    sum(increase(foundry_workitem_transition_total{state="completed"}[1h]))
    /
    sum(increase(foundry_workitem_transition_total{state=~"completed|failed"}[1h]))
  )
) / 0.001  # 0.1% error budget
```

### 3.2 Latency SLO

**Target:** 95% of workitems complete within 5 minutes.

```promql
histogram_quantile(0.95, rate(foundry_workitem_duration_seconds_bucket[1h])) < 300
```

### 3.3 Freshness SLO (Law Search)

**Target:** Law Search replicas are within 60 seconds of Librarian.

```promql
foundry_librarian_sequence_id - on() group_right foundry_lawsearch_sequence_id < 100
```

---

## 4. Prometheus Scrape Config

```yaml
scrape_configs:
  - job_name: 'foundry-friction'
    kubernetes_sd_configs:
      - role: pod
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_label_app]
        regex: flow-monitor
        action: keep
      - source_labels: [__meta_kubernetes_pod_container_port_number]
        regex: "9090"
        action: keep

  - job_name: 'foundry-operator'
    kubernetes_sd_configs:
      - role: pod
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_label_app]
        regex: foundry-operator
        action: keep

  - job_name: 'foundry-sidecars'
    kubernetes_sd_configs:
      - role: pod
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_annotation_foundry_io_sidecar]
        regex: "true"
        action: keep
      - source_labels: [__address__]
        regex: '([^:]+)(?::\d+)?'
        replacement: '${1}:35699'
        target_label: __address__
```

---

## 5. Troubleshooting Playbook

### 5.1 Gridlock Detected

**Symptoms:** Alert fires, `active_count > 0` but `transition_rate == 0`.

**Investigation:**
1. Check node readiness: `kubectl get pods -l foundry.io/flow=<flow>`
2. Check operator logs: `kubectl logs -l app=foundry-operator`
3. Look for stuck workitems: `kubectl get workitems -o wide | grep Running`
4. Check if Archivist/Librarian are healthy

**Common Causes:**
- All nodes crashed or draining
- Routing loop (workitems cycling)
- External dependency failure (LLM provider, Archivist)

### 5.2 Friction Spike

**Symptoms:** Alert fires, friction 3× above baseline.

**Investigation:**
1. Identify top contributors: Query `topk(5, rate(foundry_friction_score_total[10m]))`
2. Check for recent law changes: `kubectl get laws --sort-by=.metadata.creationTimestamp`
3. Check for node deployments: Recent image updates?
4. Review feedback loop depth on stuck workitems

**Common Causes:**
- New law creating false positives
- Node logic regression
- Upstream data quality issue

### 5.3 Zombie Node

**Symptoms:** Pod running, heartbeat misses increasing.

**Investigation:**
1. Check pod logs: `kubectl logs <pod>`
2. Check resource usage: `kubectl top pod <pod>`
3. Exec into container: `kubectl exec -it <pod> -- /bin/sh`
4. Look for blocking operations (network, disk, infinite loop)

**Common Causes:**
- Stuck on external API call
- Memory pressure causing GC stalls
- Deadlock in user code

---

## 6. Reference

See `helm/prometheus-rules.yaml` for the complete AlertManager rules implementation.
