# Governance Flow Operator: Operational Integration

## 3. Operational Integration

### 3.1 Governor Deployment

**Kubernetes Resources:**
1. **Namespace:** `governance-flow`
2. **CRDs:** Standard Foundry CRDs.
3. **Operator Deployment:**
   ```yaml
   apiVersion: apps/v1
   kind: Deployment
   metadata:
     name: governance-operator
     namespace: governance-flow
   spec:
     replicas: 2
     selector:
       matchLabels:
         app: governance-operator
     template:
       metadata:
         labels:
           app: governance-operator
       spec:
         initContainers:
           - name: sovereign-bootstrap
             image: foundry/governance-operator:3.6.0
             command: ["/bin/sh", "-c"]
             args:
               - |
                 if [ ! -f /etc/governance/keys/state-root.key ]; then
                   if [ "$OPERATOR_MODE" = "Local" ]; then
                     echo "Generating RSA-4096 state-root-keypair..."
                     openssl genrsa -out /tmp/state-root.key 4096
                     kubectl create secret generic state-root-keypair --from-file=state-root.key=/tmp/state-root.key
                     echo "Secret created. Restarting to mount volume."
                     exit 1
                   else
                     echo "Error: state-root-keypair missing and mode is not Local."
                     exit 1
                   fi
                 fi
             env:
               - name: OPERATOR_MODE
                 value: "Local"
         containers:
           - name: operator
             image: foundry/governance-operator:3.6.0
             env:
               - name: OPERATOR_MODE
                 value: "GOVERNOR"
             ports:
               - name: grpc-bureau
                 containerPort: 35698
               - name: http-bureau
                 containerPort: 8080
             livenessProbe:
               httpGet:
                 path: /healthz/liveness
                 port: 8080
             readinessProbe:
               httpGet:
                 path: /healthz/readiness
                 port: 8080
             startupProbe:
               httpGet:
                 path: /healthz/startup
                 port: 8080
             volumeMounts:
               - name: keys
                 mountPath: /etc/governance/keys
                 readOnly: true
         volumes:
           - name: keys
             secret:
               secretName: state-root-keypair
   ```

**Service Endpoints:**
* **gRPC (CSR Signing):** `governance-operator.governance-flow.svc:35698`
* **HTTP (Library Snapshot):** `governance-operator.governance-flow.svc:8080/library/snapshot.tar.gz`

### 3.1.1 RBAC Requirements

The Governor requires permissions to manage its own state and coordinate leader election for the Sovereign role.

```yaml
rules:
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["get", "create", "update"]
  - apiGroups: ["flow.gideas.io"]
    resources: ["laws"]
    verbs: ["get", "list", "watch", "create", "update", "patch"]
```

### 3.2 Sibling Operator Integration

Sibling Operators configure their Governor endpoint and trigger the **Annexation Protocol**:

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryFlow
metadata:
  name: ideate-flow
  namespace: flow-ideate
spec:
  governanceEndpoint: "governance-operator.governance-flow.svc:35698"
  federationMode: "Federated"
```

**Reference Update:** For details on the Annexation handshake, see [flow_spec/03_identity_and_federation.md](../flow_spec/03_identity_and_federation.md).
