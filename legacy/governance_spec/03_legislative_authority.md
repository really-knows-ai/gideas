# Governance Flow Operator: Legislative Authority

## 2.3 The Legislator (The Writer)

The Governor holds **exclusive write authority** for Tier 3 Law within the State.

### 2.3.1 Tier 3 Namespace Authority

**Write Permissions:**
The Governor is the **only** component allowed to create, update, or delete `Law` CRDs with label `tier: "3"` in the `governance-flow` namespace. 
* **Leader Restriction:** Only the **Elected Sovereign** (the leader replica) is permitted to perform write/update operations on `Law` CRDs.

### 2.3.2 Legislative Workflow

The Governor runs the **Governance G(IDEAS) Flow** to process Tier 3 legislation:

1. **Petition Ingress:** Sibling Flows submit `petition-v1` Workitems.
2. **Forge → Quench → Appraise:** Operational Nodes draft, validate, and refine the Law.
3. **Sort (HITL Gate):** A human legislative authority reviews and approves the draft.
4. **Refine → Publish:** The approved draft is minted as a `Law` CRD with `tier: "3"`.

**Snapshot Logic (Deterministic Builds):**
The State Library Snapshot is generated deterministically by **every replica independently**. 
* **Mechanism:** Each replica maintains a local Informer cache of Tier 3/4 Laws and rebuilds the `snapshot.tar.gz` in memory whenever the cache changes.
* **Benefit:** This allows any pod (Bureaucrat) to serve the snapshot via HTTP, enabling standard Kubernetes Service load balancing and ensuring consistency across the cluster.
