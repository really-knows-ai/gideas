# Foundry Node: Security and Health

## 2. The Security Model (Security Plane)

> **Plane:** Security (cross-cuts all other planes)

The Node operates as a **Civilian Component** within a Zero-Trust architecture. The Security Plane provides identity, authentication, and cryptographic trust across the entire Flow.

### Security Plane Components in Node Context

| Component | Security Plane Role |
|-----------|---------------------|
| **Sidecar** | Credential holder, auth broker, stamp signer |
| **ServiceAccount Token** | v1 identity (K8s-issued) |
| **mTLS Certificate** | v2 identity (Governor-issued) |
| **Passport Stamps** | Cryptographic attestation of role completion |
| **Trust Bundle** | Certificate chain for signature verification |

---

### 2.1 Network Posture

Nodes have **direct, uninhibited network access**. They call external APIs (e.g., Stripe, OpenAI, GitHub) directly using native HTTP libraries.

**Direct Access:** Network restriction and observability are Infrastructure Concerns delegated to the underlying Kubernetes NetworkPolicies and Service Mesh (e.g., Istio, Cilium). The Sidecar does not intercept traffic.

Organizations requiring network segmentation should apply Kubernetes `NetworkPolicies` at the Namespace level.

### 2.2 The Identity Boundary (Zero Trust)

Security is enforced by **Cryptographic Identity**. This is the core principle of the Security Plane.

* **The Sidecar (Security Plane Agent):** Mounts a sensitive **ServiceAccount Token** or **mTLS Certificate**. This credential allows it to authenticate with Control Plane services (Archivist, Operator, Telemetry). The Sidecar is the Security Plane's presence in the Data Plane.
* **The User Container (Data Plane):** Relies on the Sidecar to broker all authenticated requests. Application code operates without credentials.

### 2.3 Sidecar Health & Liveness Protocol

The Sidecar acts as the health proxy for the Node Pod, enabling Kubernetes to detect and terminate hung Node processes that would otherwise starve the Sidecar's ability to check Workitem status.

**The Problem:** If a Node enters an infinite loop or deadlocks while consuming 100% CPU, the Sidecar might not be able to poll the Workitem CRD to detect a `Failed` state transition. The Reaper marks the Workitem as failed, but the Pod continues running, consuming resources and blocking work assignment.

**The Solution:** The Kubelet (Kubernetes hypervisor) monitors Pod health via standardized probes. The Sidecar exposes a health endpoint that fails when the Node becomes unresponsive.

#### Heartbeat Interface

The Sidecar tracks Node activity through a dual-mode protocol. **Activity signals propagate to the Workitem CRD** to extend the Operator's execution lease.

**Implicit Heartbeat (Automatic):**
- **Tracked Operations:** The Sidecar monitors **any** RPC call from the Node: `StampArtefact`, `RecordFinding`, `RecordTelemetry`, `ResolveFeedback`, `CreateWorkitem`, `CompleteWorkitem`, `SearchLibrary`, etc.
- **Mechanism:** On each inbound RPC, the Sidecar automatically updates the local `last_node_activity_timestamp` AND propagates to the Workitem CRD's `status.lastActivityAt` field (throttled - see below).
- **Developer Impact:** Zero. Every SDK call implicitly signals "I am alive" without requiring explicit heartbeat management.

**Explicit Heartbeat (Manual):**
- **Purpose:** For long-running processes (e.g., complex reasoning loops, multi-step LLM chains) where no other SDK calls are made for extended periods.
- **Mechanism:** The Node calls the explicit `Heartbeat` RPC to reset the activity timer and extend the Operator lease.
- **Requirement:** MUST be called at least once every `node_specific_timeout` during blocking operations to prevent inactivity timeout.

**CRD Propagation (Lease Extension):**

To protect etcd from write storms, the Sidecar throttles CRD patches:

```
Throttle Window = Node.spec.timeout / 3
(e.g., 60s timeout → patch at most every 20s)

IF (Now - last_crd_patch_time) > throttle_window THEN
    PATCH Workitem.status.lastActivityAt = Now()
    last_crd_patch_time = Now()
```

This ensures the Operator always has a recent `lastActivityAt` value while avoiding excessive etcd writes.

> **Recommendation:** For LLM-based workloads, use the `FoundryAgent` pattern (see [06_foundry_agent.md](./06_foundry_agent.md)) which handles heartbeat management automatically during inference. Manual heartbeat is only required for non-LLM long-running operations or custom inference implementations.

**Timeout Window:** The window is no longer fixed at 30 seconds. It is determined by the following priority:
1. The `timeout` defined in the `FoundryNode` CRD.
2. The `standardNodeTimeout` defined in the `FoundryFlow` configuration.
3. A hardcoded system fallback of 30 seconds.

#### Health & Readiness Endpoints (The Triad Model)
**Port Allocation (Sidecar):**
| Port | Purpose |
|------|--------|
| 35697 | Node ↔ Sidecar gRPC (Session API) |
| 35699 | Health, Readiness & Metrics (HTTP) |

The Sidecar exposes multiple HTTP endpoints on port **35699**:

| Endpoint | Purpose |
|----------|--------|
| `/healthz` | Liveness probe - "Is the process alive?" |
| `/readyz` | Readiness probe - "Can the node accept work?" |
| `/metrics` | Prometheus metrics endpoint for observability |

This consolidation simplifies network policy configuration and service mesh integration. All three endpoints are lightweight and return cached state.

**A. Liveness Probe (`/healthz`) - "The Pulse"**
* **Question:** *Is the gRPC session between Sidecar and User Container active?*
* **Logic:**
    * **Check:** Is the gRPC Session active between Sidecar and User Container?
    * **Return:** `200 OK` (Always, as long as connection exists).
* **Note:** If the Node process crashes, the Sidecar's TCP connection closes. The Kubelet will fail to connect to the Sidecar, triggering a restart.

> **Design Decision:** Liveness is strictly about infrastructure health ("is the process alive?"). Timeout enforcement for application logic is delegated to the Sidecar's Timeout Enforcement Protocol (see Section 2.4).

#### Separation of Concerns: Probes vs Timeouts

**Workitem timeout and Pod liveness are completely independent systems.** A timed-out Workitem does NOT kill the Pod. A dead Pod does NOT directly fail the Workitem (the Reaper does, via inactivity detection).

| System | Question | Scope | Failure Action |
|--------|----------|-------|----------------|
| **Liveness (`/healthz`)** | Is the Node process alive? | **Pod** | Kubelet restarts Pod |
| **Readiness (`/readyz`)** | Can Node accept new work? | **Pod** | Operator stops routing to Pod |
| **Inactivity Timeout** | Is the Workitem progressing? | **Workitem** | Workitem marked `Failed` (Pod unchanged) |

**Key Implications:**

1. **Timeout → Failed Workitem, NOT Failed Pod:**
   - When a Workitem times out, the Sidecar marks it `Failed` and the Pod becomes `Ready` again
   - The Pod is NOT restarted; it can immediately accept new work
   - Debug data (logs, heap dumps) is preserved

2. **Dead Pod → Eventual Failed Workitem:**
   - If the Pod crashes (OOMKill, segfault), the Workitem is orphaned
   - The Operator's Reaper detects inactivity (`lastActivityAt` goes stale)
   - After `timeout` seconds of silence, Workitem is marked `Failed`

3. **Long-Running Active Tasks:**
   - A 5-minute LLM call with heartbeats will NOT timeout
   - Liveness probe remains healthy (gRPC session active)
   - Readiness probe returns `503` (at capacity)
   - Both probes are unaffected by Workitem duration

**B. Readiness Probe (`/readyz`) - "The Gate"**
* **Question:** *Is the Node available to accept a new Workitem?*
* **Logic:**
    * **On Boot:** Return `503 Service Unavailable`.
    * **Initial Ready:** Flip to `200 OK` upon receiving the **first Heartbeat** or an explicit `signal_ready()` RPC from the Node.
    * **Capacity Check:** The Sidecar tracks `activeWorkitemCount` (number of in-flight workitems).
      * If `concurrency == 0` (unlimited): Always return `200 OK` (after initial ready).
      * If `activeWorkitemCount < concurrency`: Return `200 OK`.
      * If `activeWorkitemCount >= concurrency`: Return `503 Service Unavailable`.
    * **Draining Override:** If SIGTERM received, always return `503` regardless of capacity.

**Implementation Note:** These endpoints must be extremely lightweight (< 1ms response time) to avoid impacting Node execution. They should return cached state, not perform I/O.

**Concurrency Handling:** When `concurrency > 1`, the Sidecar maintains a map of `workitemID → session` (timeout timer, gRPC context). Each `ProcessWorkitem` call spawns an independent handler goroutine. Node handlers MUST be concurrent-safe when `concurrency > 1`.

> Selection Participation: The Flow Operator’s Router Guard only assigns Workitems to pods that currently return `200 OK` from `/readyz`. Readiness therefore directly gates participation in node selection. See the Node Selection distribution policy in [flow_spec/01_operator_and_routing.md](../flow_spec/01_operator_and_routing.md).

#### Graceful Termination Protocol (SIGTERM Handler)

The Sidecar implements a **"Draining Mode"** to distinguish between scale-down events and genuine failures. This prevents false negatives where healthy workitems are marked as `Failed` simply because the node was decommissioned.

**SIGTERM Handler Sequence: "Lock, Assess, Wait"**

1. **LOCK (The Do Not Disturb Sign):**
   - **Action:** Immediately force the `/readyz` endpoint to return `503 Service Unavailable`.
   - **Constraint:** This overrides any derived readiness state. It prevents the Operator from assigning *new* work during shutdown.

2. **ASSESS (The Busy Check):**
   - **Check:** Is `activeWorkitemCount > 0`?
   - **If Idle:** Exit process immediately (Exit Code 0).
   - **If Busy:** Log `State: Draining, active_workitems: N` and enter **Wait Mode**.

3. **WAIT (The Patience):**
   - **Action:** Keep the process alive. Wait for ALL active workitems to reach terminal state (`Completed` or `Failed`).
   - **Constraint:** Strictly reject any *new* `ProcessWorkitem` RPC calls during this phase (though the `503` Lock should prevent them from arriving).
   - **Exit Condition:** Once `activeWorkitemCount == 0`, exit process immediately (Exit Code 0).
   - **Fail-Safe:** If any workitem's `node_timeout` expires during the wait, the existing internal watchdog will trigger a failure for that workitem. Process exits once all workitems are finalized.

**Impact:**
- `Failed` status strictly indicates application or timeout failures.
- Scale-down operations take longer (up to `node_timeout`), but zero work is lost.
- The `2×` grace period (set by Operator) ensures no "Zombie Gap" where infrastructure kills a node about to report success.

### 2.4 Timeout Enforcement Protocol (Activity-Based)

The Sidecar enforces Workitem **inactivity** deadlines **independently** of Pod liveness. This decoupling ensures that "slow" does not mean "dead" - active nodes remain alive indefinitely.

**Mechanism:** The Sidecar maintains an `InactivityTimer` for the active Workitem. The timer **resets on every activity** (SDK call or explicit heartbeat).

```
Inactivity Timer Logic:
- Initialized to Node.spec.timeout when Workitem assigned
- Reset to Node.spec.timeout on EVERY SDK call or heartbeat
- Expires only if no activity for the full timeout duration
```

**Critical Implication:** A 5-minute LLM inference with 15-second heartbeats will complete successfully, even with a 60-second timeout. The timeout measures **idle time**, not **total time**.

**Action on Expiry (Inactivity Detected):**

1. **Cancel Context:** Sidecar cancels the gRPC Context sent to the Node. The User code receives `ctx.Done()` and should terminate gracefully.
2. **Report Failure:** Sidecar sends `WorkitemResult{Failed, Reason: "TIMEOUT"}` to the Operator via the Workitem CRD.
3. **Preserve Pod:** Sidecar **does not** fail the liveness probe. The Pod remains `Running` and transitions to `Ready` once the handler exits.

> **Rationale:** This architecture preserves debug data (logs, stack traces) that would otherwise be destroyed by a `SIGKILL`. It also avoids the resource cost of restarting a pod for a logic-level failure.

---

**Operator Integration Note (Crash Signals):** The Operator infers Workitem state from inactivity detection only. Pod crash signals (e.g., `OOMKilled`, `Error`) to transition Workitem state. Crashes and slowness are treated uniformly: if the deadline elapses without completion, the Reaper enforces failure or escalation. See the crash-handling philosophy in [flow_spec/01_operator_and_routing.md](../flow_spec/01_operator_and_routing.md).

**Kubelet Integration:** The Operator injects three distinct probes into the `FoundryNode` Pod Spec to eliminate the "Zombie Gap" and protect slow-booting nodes.

**Probe Configuration:**
The Flow Operator must update the Pod generation template to use these fixed and dynamic values:

```yaml
# 1. Liveness: Start Checking Immediately (No Zombie Gap)
livenessProbe:
  httpGet: { path: /healthz, port: 35699 }
  initialDelaySeconds: 5
  periodSeconds: 10
  failureThreshold: 3

# 2. Readiness: Gate Traffic until Explicitly Ready
readinessProbe:
  httpGet: { path: /readyz, port: 35699 }
  initialDelaySeconds: 5
  periodSeconds: 5

# 3. Startup: Protect Slow Boots (The "Deep Boot" Safety Net)
startupProbe:
  httpGet: { path: /readyz, port: 35699 } # Checks Readiness, not Liveness
  failureThreshold: 60  # 60 * 10s = 10 Minutes to boot
  periodSeconds: 10
```
