/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	flowv1gen "github.com/foundry/flow/gen/flow/v1"
	flowv1 "github.com/foundry/flow/operator/api/v1"
)

// FoundryGraphReconciler reconciles a FoundryGraph object.
type FoundryGraphReconciler struct {
	client.Client
	Scheme                    *runtime.Scheme
	OperatorNamespace         string
	CartographerPort          int32
	ReadinessTimeout          time.Duration
	CartographerImage         string
	EventBusAddress           string
	CapabilityStalenessWindow string
	ProxyRoutingTable         *ProxyRoutingTable
	CartographerDialer        func(ctx context.Context, endpoint string) (CartographerClient, error)
}

// +kubebuilder:rbac:groups=flow.foundry.io,resources=foundrygraphs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=flow.foundry.io,resources=foundrygraphs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=flow.foundry.io,resources=foundrygraphs/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create
// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create

// lastAppliedSpecAnnotation is the annotation key for storing the last-applied spec.
const lastAppliedSpecAnnotation = "flow.foundry.io/cartographer-last-applied-spec"

// finalizerName is the finalizer for FoundryGraph cleanup.
const finalizerName = "flow.foundry.io/cartographer-cleanup"

// errWipeBlockedByOpenTransactions is the sentinel returned when a destructive
// schema change's WipeGraph call fails with FAILED_PRECONDITION because open
// transactions exist. Only this error warrants the DestructiveChangeBlocked
// condition (SPEC R1/R6).
var errWipeBlockedByOpenTransactions = errors.New("wipe blocked by open transactions")

// errExistingPodUnreachable is the sentinel returned by applySchemaOnExisting when the
// existing Cartographer pod cannot be reached (dial failure or HealthCheck failure). It
// is the one schema-diff error class that must NOT short-circuit the reconcile: the pod
// may be unreachable precisely because the Deployment is missing (deleted or failed to
// schedule), and returning early at the schema-diff branch would wedge every requeue
// before reconcileDeployment is reached — the Deployment could never be recreated. The
// Reconcile schema-diff branch falls through to the infra steps on this sentinel so the
// Deployment is restored and the step-10 ApplySchema applies the (possibly updated)
// schema on the new pod (SPEC R6 reconcile-to-desired-state; the 10m periodic resync is
// the independent safety net).
var errExistingPodUnreachable = errors.New("existing cartographer pod unreachable")

// grpcCallTimeout bounds each Cartographer RPC phase issued by the reconciler. The
// controller-runtime reconcile ctx carries no per-reconcile deadline (only manager
// cancellation), so a slow or blackholed Cartographer would otherwise hang the reconcile
// indefinitely rather than failing fast into the SPEC R6 requeue-with-backoff path.
const grpcCallTimeout = 30 * time.Second

// readinessBackoffBase and readinessBackoffMax are the SPEC R6 step-5 readiness-failure
// backoff parameters: exponential backoff with an initial delay of ~5s, doubling per
// attempt, capped at 5m ("Reconcile() returns an error — controller-runtime re-queues the
// request with exponential backoff (initial delay ~5s, doubling per attempt, capped at
// 5m)"). Configured on the FoundryGraph controller's workqueue rate limiter below so the
// deployed operator uses these parameters instead of controller-runtime's defaults (5ms
// initial, 1000s cap).
const (
	readinessBackoffBase = 5 * time.Second
	readinessBackoffMax  = 5 * time.Minute
)

// readinessRateLimiter returns the workqueue rate limiter for the FoundryGraph
// controller's error requeues, implementing the SPEC R6 step-5 backoff parameters
// (initial ~5s, doubling per attempt, capped at 5m).
func readinessRateLimiter() workqueue.TypedRateLimiter[reconcile.Request] {
	return workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](readinessBackoffBase, readinessBackoffMax)
}

// Reconcile implements the main reconciliation loop for FoundryGraph.
func (r *FoundryGraphReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Step 1: Fetch the FoundryGraph CR.
	var fg flowv1.FoundryGraph
	if err := r.Get(ctx, req.NamespacedName, &fg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Reconciling FoundryGraph", "name", fg.Name, "namespace", fg.Namespace)

	// Step 2: Finalizer check (deletion path).
	if fg.DeletionTimestamp != nil && !fg.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&fg, finalizerName) {
			log.Info("FoundryGraph is being deleted, running tear-down")
			if err := r.tearDown(ctx, &fg); err != nil {
				return ctrl.Result{}, fmt.Errorf("tear-down: %w", err)
			}
			controllerutil.RemoveFinalizer(&fg, finalizerName)
			if err := r.Update(ctx, &fg); err != nil {
				return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	// Step 3: Ensure finalizer.
	if !controllerutil.ContainsFinalizer(&fg, finalizerName) {
		controllerutil.AddFinalizer(&fg, finalizerName)
		if err := r.Update(ctx, &fg); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
	}

	// Step 3.5: R1 singleton enforcement — at most one FoundryGraph per namespace. The
	// earliest-created FoundryGraph is the namespace owner and is provisioned; any later
	// one is a conflict: the Operator does not provision a Cartographer for it, does not
	// populate status.endpoint, and sets a FoundryGraphConflict condition (SPEC R1).
	if conflict, err := r.enforceSingleton(ctx, &fg); err != nil {
		return r.setFailedCondition(ctx, &fg, err)
	} else if conflict {
		return r.setConflictCondition(ctx, &fg)
	}

	// Determine schema diff if a previous spec exists.
	var oldSpec *flowv1.FoundryGraphSpec
	diffResult := SchemaDiffNone
	oldSpecJSON, ok := fg.Annotations[lastAppliedSpecAnnotation]
	if ok && oldSpecJSON != "" {
		oldSpec = &flowv1.FoundryGraphSpec{}
		if err := json.Unmarshal([]byte(oldSpecJSON), oldSpec); err != nil {
			log.Error(err, "failed to deserialize last-applied spec annotation, treating as first reconcile")
			oldSpec = nil
		}
	}
	if oldSpec != nil {
		if !specSemanticallyEqual(oldSpec, &fg.Spec) {
			diffResult = diffSchema(oldSpec, &fg.Spec)
		}
	}

	// Build the spec map for comparison at reconcile start.
	currentSpec := *fg.Spec.DeepCopy()

	// Branching logic for spec changes (see PHASE_05 D1 branching pseudocode).
	switch diffResult {
	case SchemaDiffDestructive:
		// Destructive: HealthCheck -> WipeGraph -> ApplySchema on existing pod.
		if err := r.applySchemaOnExisting(ctx, &fg, true); err != nil {
			// SPEC R6: the DestructiveChangeBlocked condition is reserved for the
			// single case where WipeGraph fails with FAILED_PRECONDITION because
			// open transactions exist. All other destructive-path errors (dial,
			// HealthCheck, WipeGraph INTERNAL, ApplySchema) are ordinary failures.
			if errors.Is(err, errWipeBlockedByOpenTransactions) {
				return r.setBlockedCondition(ctx, &fg, err)
			}
			if errors.Is(err, errExistingPodUnreachable) {
				// The existing pod is unreachable (dial or HealthCheck failed) — the
				// Deployment may be missing (deleted or failed to schedule). Do NOT
				// short-circuit: fall through to the infra steps so reconcileDeployment
				// restores it, then the step-10 ApplySchema applies the (possibly
				// updated) schema on the new pod. Returning early here would wedge every
				// requeue before reconcileDeployment is reached — the Deployment could
				// never be recreated (SPEC R6 reconcile-to-desired-state; the 10m
				// periodic resync is the independent safety net).
				log.Info("Existing Cartographer pod unreachable during schema change; continuing to reconcile infrastructure", "err", err)
				break
			}
			return r.setFailedCondition(ctx, &fg, err)
		}
	case SchemaDiffNonDestructive:
		// Non-destructive: HealthCheck -> ApplySchema on existing pod.
		if err := r.applySchemaOnExisting(ctx, &fg, false); err != nil {
			if errors.Is(err, errExistingPodUnreachable) {
				log.Info("Existing Cartographer pod unreachable during schema change; continuing to reconcile infrastructure", "err", err)
				break
			}
			return r.setFailedCondition(ctx, &fg, err)
		}
	}

	// Steps 4-8: Reconcile infrastructure.
	if err := r.reconcileInfrastructure(ctx, &fg); err != nil {
		return r.setFailedCondition(ctx, &fg, err)
	}

	// Step 9: Wait for readiness.
	if err := r.waitForReadiness(ctx, &fg); err != nil {
		// SPEC R6 step 5: a readiness timeout/cancellation surfaces as an error so
		// controller-runtime re-queues the request with exponential backoff. Returning a
		// bare (Result{}, err) is the requeue-with-backoff path.
		return ctrl.Result{}, fmt.Errorf("readiness: %w", err)
	}

	// Step 10: ApplySchema on new pod. Idempotent — no-op if schema already applied.
	// Apply the reconcile-start snapshot (currentSpec) — the same spec the schema diff
	// and the last-applied-spec annotation reference — never a re-fetched latest spec:
	// a spec edited mid-reconcile must not be applied while the annotation records a
	// different snapshot, or the next reconcile re-detects a phantom diff and, for
	// destructive changes, re-runs WipeGraph (wiping graph data written since the
	// first apply). Any mid-reconcile edit is picked up by the next reconcile's diff,
	// which re-evaluates the destructive decision against the edited spec.
	if err := r.applySchema(ctx, &fg, &currentSpec); err != nil {
		return r.setFailedCondition(ctx, &fg, err)
	}

	// Step 11: Update status.
	if err := r.updateStatus(ctx, &fg, &currentSpec); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	// Step 12: Register proxy route.
	r.registerProxyRoute(&fg)

	// Step 13: Set Ready condition.
	return r.setReadyCondition(ctx, &fg)
}

// enforceSingleton reports whether this FoundryGraph is a namespace singleton conflict
// (SPEC R1: at most one FoundryGraph per namespace). The earliest-created FoundryGraph
// in the namespace is the owner and is provisioned; every other one is a conflict and
// must not be provisioned. Creation timestamp ordering with name as tiebreak keeps the
// owner deterministic when two resources are created together (or in the zero-timestamp
// fake-client case).
func (r *FoundryGraphReconciler) enforceSingleton(ctx context.Context, fg *flowv1.FoundryGraph) (bool, error) {
	var list flowv1.FoundryGraphList
	if err := r.List(ctx, &list, client.InNamespace(fg.Namespace)); err != nil {
		return false, fmt.Errorf("list FoundryGraphs for singleton enforcement: %w", err)
	}
	if len(list.Items) <= 1 {
		return false, nil
	}
	ownerName := ""
	var ownerTime metav1.Time
	for i := range list.Items {
		it := &list.Items[i]
		if ownerName == "" || it.CreationTimestamp.Before(&ownerTime) ||
			(it.CreationTimestamp.Equal(&ownerTime) && it.Name < ownerName) {
			ownerName = it.Name
			ownerTime = it.CreationTimestamp
		}
	}
	return fg.Name != ownerName, nil
}

// setConflictCondition sets the FoundryGraphConflict condition on a FoundryGraph that is
// not the namespace's singleton owner (SPEC R1) and returns without an error so the
// conflict is not re-queued with exponential backoff — resolution is user action (delete
// one of the FoundryGraphs). The 10m RequeueAfter matches the Ready path cadence so owner
// promotion is re-evaluated promptly after the namespace owner is deleted instead of
// waiting for the manager's informer resync (controller-runtime default ~10h) with a
// stale FoundryGraphConflict condition in the meantime.
func (r *FoundryGraphReconciler) setConflictCondition(ctx context.Context, fg *flowv1.FoundryGraph) (ctrl.Result, error) {
	if err := r.Get(ctx, client.ObjectKeyFromObject(fg), fg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	meta.SetStatusCondition(&fg.Status.Conditions, metav1.Condition{
		Type:               "FoundryGraphConflict",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: fg.Generation,
		Reason:             "SingletonViolation",
		Message:            fmt.Sprintf("a FoundryGraph already exists in namespace %s; a namespace may contain at most one FoundryGraph", fg.Namespace),
	})
	if err := r.Status().Update(ctx, fg); err != nil {
		return ctrl.Result{}, fmt.Errorf("set conflict condition: %w", err)
	}
	return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
}

// applySchemaOnExisting applies schema changes to the existing Cartographer pod.
// If destructive is true, calls WipeGraph first.
func (r *FoundryGraphReconciler) applySchemaOnExisting(ctx context.Context, fg *flowv1.FoundryGraph, destructive bool) error {
	// Dial the existing cartographer pod.
	endpoint := fmt.Sprintf("%s.%s.svc.cluster.local:%d", r.cartographerServiceName(fg), fg.Namespace, r.CartographerPort)
	cc, err := r.CartographerDialer(ctx, endpoint)
	if err != nil {
		// Marked with errExistingPodUnreachable: the pod may be down because the
		// Deployment is missing, so Reconcile must not short-circuit on this error —
		// it falls through to the infra steps to restore the Deployment.
		return fmt.Errorf("%w: dial existing cartographer: %v", errExistingPodUnreachable, err)
	}
	defer func() { _ = cc.Close() }()

	// Bound the RPC phase with a per-call deadline: the reconcile ctx has no deadline (only
	// manager cancellation), so without this a blackholed Cartographer hangs the reconcile
	// instead of failing fast into the SPEC R6 requeue-with-backoff path.
	rpcCtx, rpcCancel := context.WithTimeout(ctx, grpcCallTimeout)
	defer rpcCancel()

	// HealthCheck
	if _, err := cc.HealthCheck(rpcCtx, &flowv1gen.HealthCheckRequest{}); err != nil {
		return fmt.Errorf("%w: health check on existing pod: %v", errExistingPodUnreachable, err)
	}

	if destructive {
		// WipeGraph
		if _, err := cc.WipeGraph(rpcCtx, &flowv1gen.WipeGraphRequest{}); err != nil {
			if isFailedPrecondition(err) {
				// DISTINCT SENTINEL: only this case (WipeGraph blocked by open
				// transactions) deserves the DestructiveChangeBlocked condition.
				return fmt.Errorf("%w: %v", errWipeBlockedByOpenTransactions, err)
			}
			return fmt.Errorf("wipe graph: %w", err)
		}

		// Persist the last-applied-spec annotation BEFORE ApplySchema so the new schema
		// never becomes active while the annotation still records the old spec. Without
		// this, a crash between the existing-pod WipeGraph+ApplySchema and the
		// updateStatus annotation persist re-detects the destructive diff on the next
		// reconcile and re-runs WipeGraph — silently deleting graph data written under
		// the newly-applied schema in the interim. The persist sits AFTER WipeGraph (not
		// before): persisting before the wipe would record the new spec as applied while
		// the store still holds the old schema, and a crash in that window would suppress
		// the destructive diff forever — step-10 ApplySchema of a subset schema without a
		// prior wipe returns FAILED_PRECONDITION (SPEC R1/R2), wedging the change. With
		// the persist between wipe and apply, a crash after the persist converges via the
		// already-wiped store (no re-wipe), and a crash before it re-wipes only
		// old-schema data — data written under the new schema is impossible because the
		// apply never ran.
		if err := r.persistLastAppliedSpec(ctx, fg, &fg.Spec); err != nil {
			return fmt.Errorf("persist last-applied-spec annotation before destructive ApplySchema: %w", err)
		}
	}

	// ApplySchema
	schema := r.schemaFromCRD(&fg.Spec)
	if _, err := cc.ApplySchema(rpcCtx, &flowv1gen.ApplySchemaRequest{Schema: schema}); err != nil {
		return fmt.Errorf("apply schema on existing pod: %w", err)
	}

	return nil
}

// persistLastAppliedSpec persists the given spec in the lastAppliedSpecAnnotation. The
// annotation is metadata and must go through the main Update — the status subresource
// does not carry annotations. It is the marker the next reconcile's schema diff is
// computed against, so it must record the same spec the apply actually pushed. It is
// persisted not only at updateStatus (reconcile end) but also mid-reconcile on the
// destructive path (between WipeGraph and ApplySchema) so a crash can never leave the
// new schema active while the annotation still records the old spec.
func (r *FoundryGraphReconciler) persistLastAppliedSpec(ctx context.Context, fg *flowv1.FoundryGraph, spec *flowv1.FoundryGraphSpec) error {
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal spec: %w", err)
	}
	if fg.Annotations == nil {
		fg.Annotations = make(map[string]string)
	}
	fg.Annotations[lastAppliedSpecAnnotation] = string(specJSON)
	if err := r.Update(ctx, fg); err != nil {
		return fmt.Errorf("update FoundryGraph: %w", err)
	}
	return nil
}

// applySchema applies the reconcile-start schema snapshot to a (newly-ready)
// Cartographer pod. The schema is built from `spec` — the same snapshot the schema
// diff, the WipeGraph decision, and the last-applied-spec annotation reference — NOT
// from the re-fetched CR's latest spec. Applying the latest spec while the diff (and
// the annotation) reference the reconcile-start snapshot would let a spec edited
// mid-reconcile be applied while the annotation records a different spec; the next
// reconcile would re-detect a phantom diff and, for destructive changes, re-run
// WipeGraph — wiping graph data written since the first apply. The CR is still
// re-fetched so a concurrently-deleted CR cannot fall through as a zero-valued object
// and have an empty schema dialed+applied against a live Cartographer (SPEC R6 step 6:
// ApplySchema runs only on the CR this reconciler owns), but only the object's
// existence is checked — the schema applied is the reconcile-start snapshot. Any
// mid-reconcile edit is picked up by the next reconcile's diff, which re-evaluates the
// destructive decision against the edited spec.
func (r *FoundryGraphReconciler) applySchema(ctx context.Context, fg *flowv1.FoundryGraph, spec *flowv1.FoundryGraphSpec) error {
	// Re-fetch the CR purely as a concurrent-deletion guard (a deleted CR must not
	// fall through as a zero-valued object and have a schema applied to a live
	// Cartographer). The re-fetched spec is intentionally not used: the schema applied
	// below is the reconcile-start snapshot so diff, apply, and annotation stay in sync.
	if err := r.Get(ctx, client.ObjectKeyFromObject(fg), fg); err != nil {
		return fmt.Errorf("re-fetch FoundryGraph before ApplySchema: %w", err)
	}

	endpoint := fmt.Sprintf("%s.%s.svc.cluster.local:%d", r.cartographerServiceName(fg), fg.Namespace, r.CartographerPort)
	c, err := r.CartographerDialer(ctx, endpoint)
	if err != nil {
		return fmt.Errorf("dial cartographer: %w", err)
	}
	defer func() { _ = c.Close() }()

	// Bound the RPC phase with a per-call deadline (the reconcile ctx has no deadline).
	rpcCtx, rpcCancel := context.WithTimeout(ctx, grpcCallTimeout)
	defer rpcCancel()

	// HealthCheck
	if _, err := c.HealthCheck(rpcCtx, &flowv1gen.HealthCheckRequest{}); err != nil {
		return fmt.Errorf("health check: %w", err)
	}

	// ApplySchema
	schema := r.schemaFromCRD(spec)
	if _, err := c.ApplySchema(rpcCtx, &flowv1gen.ApplySchemaRequest{Schema: schema}); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}

	return nil
}

// isFailedPrecondition checks if a gRPC error is FAILED_PRECONDITION.
func isFailedPrecondition(err error) bool {
	return status.Code(err) == codes.FailedPrecondition
}

// waitForReadiness polls the Deployment until every desired replica is ready on the
// current pod template or the timeout elapses. The readiness timeout is the sole
// termination condition: transient Get errors (e.g. a Deployment momentarily not yet
// visible after CreateOrUpdate) do not short-circuit the poll. allReplicasReady requires
// UpdatedReplicas so a spec-change rollout waits for the NEW pod to pass readiness rather
// than counting the old ReplicaSet's ready pod (SPEC R6: schema re-apply runs only after
// the new pod passes its readiness probe).
func (r *FoundryGraphReconciler) waitForReadiness(ctx context.Context, fg *flowv1.FoundryGraph) error {
	log := logf.FromContext(ctx)
	deployName := "cartographer-" + fg.Name
	nn := types.NamespacedName{Name: deployName, Namespace: fg.Namespace}

	deadline := time.Now().Add(r.ReadinessTimeout)
	// Scale the poll cadence with the configured timeout so a small overridden
	// timeout still polls promptly instead of paying up to a fixed 5s of
	// granularity; capped at 5s so the default (5m) timeout keeps the current cadence.
	pollInterval := 5 * time.Second
	if half := r.ReadinessTimeout / 2; half > 0 && half < pollInterval {
		pollInterval = half
	}

	for time.Now().Before(deadline) {
		var deploy appsv1.Deployment
		if err := r.Get(ctx, nn, &deploy); err != nil {
			// Keep polling until the timeout; the Deployment may not be visible yet.
			log.Info("waiting for cartographer deployment", "deployment", deployName, "err", err)
		} else if allReplicasReady(&deploy) {
			log.Info("Cartographer pod is ready", "deployment", deployName)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
	return fmt.Errorf("readiness timeout (%v) exceeded for deployment %s", r.ReadinessTimeout, deployName)
}

// allReplicasReady reports whether every desired replica is ready AND running the current
// pod template (SPEC: "all replicas ready, readiness probe passing"; R6 spec-change flow:
// "After the new pod passes its readiness probe, the Operator re-applies the current
// schema"). Requiring UpdatedReplicas is what guarantees the ready pod is the NEW one: on
// a spec-change rollout the old ReplicaSet keeps ReadyReplicas>=1 while the new pod is
// still starting, so ReadyReplicas alone would let the step-10 ApplySchema dial the
// ClusterIP Service and hit the old pod — the new pod would start without the updated
// rules until the next resync. Using Replicas/ReadyReplicas/UpdatedReplicas rather than
// AvailableReplicas > 0 keeps the check correct even if the replica count ever diverges
// from the hardcoded value in the Deployment.
func allReplicasReady(deploy *appsv1.Deployment) bool {
	if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas == 0 {
		return false
	}
	return deploy.Status.ReadyReplicas >= *deploy.Spec.Replicas &&
		deploy.Status.UpdatedReplicas >= *deploy.Spec.Replicas
}

// updateStatus sets the endpoint, storageSize, and last-applied-spec annotation.
func (r *FoundryGraphReconciler) updateStatus(ctx context.Context, fg *flowv1.FoundryGraph, currentSpec *flowv1.FoundryGraphSpec) error {
	// Re-fetch to get latest resourceVersion.
	if err := r.Get(ctx, client.ObjectKeyFromObject(fg), fg); err != nil {
		return client.IgnoreNotFound(err)
	}

	// The last-applied-spec annotation is metadata and must go through the main Update —
	// the status subresource does not carry annotations. The main Update replaces any
	// status on the in-memory object with the stored status, so persist the annotation
	// first and set the status on a subsequent pass.
	if err := r.persistLastAppliedSpec(ctx, fg, currentSpec); err != nil {
		return err
	}

	// Set endpoint.
	if err := r.Get(ctx, client.ObjectKeyFromObject(fg), fg); err != nil {
		return client.IgnoreNotFound(err)
	}
	fg.Status.Endpoint.Host = fmt.Sprintf("cartographer-%s.%s.svc.cluster.local", fg.Name, fg.Namespace)
	fg.Status.Endpoint.Port = r.CartographerPort

	// Set storage size from PVC.
	pvc := &corev1.PersistentVolumeClaim{}
	pvcKey := types.NamespacedName{Name: "data-" + fg.Name, Namespace: fg.Namespace}
	if err := r.Get(ctx, pvcKey, pvc); err == nil {
		if storage, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
			s := storage.DeepCopy()
			fg.Status.StorageSize = &s
		}
	} else if !apierrors.IsNotFound(err) {
		// Any real error (RBAC/apiserver/transient) reading the PVC must surface to the
		// requeue path rather than silently leaving status.storageSize stale (SPEC R6 step 7).
		// IsNotFound is the only swallowed case: no PVC yet means "absent", and the default
		// storageSize remains.
		return fmt.Errorf("read pvc for storageSize: %w", err)
	}

	// Persist the status block (endpoint, storageSize) via the status subresource. The
	// CRD declares +kubebuilder:subresource:status, so the apiserver silently discards
	// status mutations on the main Update; the set*Condition helpers all use
	// Status().Update and this must be consistent with them.
	if err := r.Status().Update(ctx, fg); err != nil {
		return fmt.Errorf("update FoundryGraph status: %w", err)
	}

	return nil
}

// registerProxyRoute registers the Cartographer endpoint in the proxy routing table.
func (r *FoundryGraphReconciler) registerProxyRoute(fg *flowv1.FoundryGraph) {
	endpoint := fmt.Sprintf("cartographer-%s.%s.svc.cluster.local:%d", fg.Name, fg.Namespace, r.CartographerPort)
	r.ProxyRoutingTable.Register(fg.Namespace, fg.Name, endpoint)
}

// tearDown handles deletion cleanup.
func (r *FoundryGraphReconciler) tearDown(ctx context.Context, fg *flowv1.FoundryGraph) error {
	log := logf.FromContext(ctx)

	// Deregister proxy route.
	r.ProxyRoutingTable.Deregister(fg.Namespace, fg.Name)

	// Delete resources in order.
	resources := []client.Object{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "cartographer-" + fg.Name, Namespace: fg.Namespace}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "cartographer-" + fg.Name, Namespace: fg.Namespace}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "cartographer-" + fg.Name, Namespace: fg.Namespace}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "cartographer-" + fg.Name + "-key-reader", Namespace: fg.Namespace}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "cartographer-" + fg.Name + "-key-reader", Namespace: fg.Namespace}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "cartographer-" + fg.Name + "-remote-auth", Namespace: fg.Namespace}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "cartographer-" + fg.Name + "-remote-auth", Namespace: fg.Namespace}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data-" + fg.Name, Namespace: fg.Namespace}},
	}

	for _, res := range resources {
		if err := r.Delete(ctx, res); err != nil {
			if !apierrors.IsNotFound(err) {
				log.Error(err, "failed to delete resource during tear-down", "resource", res)
				return err
			}
		}
	}

	// Note: shared key Secrets (cartographer-operator-key, cartographer-sidecar-key) are
	// intentionally not deleted — they are created once per namespace and reused.
	log.Info("Tear-down complete", "namespace", fg.Namespace, "name", fg.Name)
	return nil
}

// setReadyCondition sets the Ready condition to True.
func (r *FoundryGraphReconciler) setReadyCondition(ctx context.Context, fg *flowv1.FoundryGraph) (ctrl.Result, error) {
	// Re-fetch to get latest resourceVersion.
	if err := r.Get(ctx, client.ObjectKeyFromObject(fg), fg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Clear prior blocking conditions.
	// Remove any stale DestructiveChangeBlocked condition set by a previous
	// destructive-change attempt that has since recovered, and any stale
	// FoundryGraphConflict set while this resource was not the namespace singleton
	// owner (SPEC R1) — reaching Ready means it is provisioned as the owner.
	meta.RemoveStatusCondition(&fg.Status.Conditions, "DestructiveChangeBlocked")
	meta.RemoveStatusCondition(&fg.Status.Conditions, "FoundryGraphConflict")
	meta.SetStatusCondition(&fg.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: fg.Generation,
		Reason:             reasonReconciled,
		Message:            "FoundryGraph reconciliation completed successfully",
	})

	if err := r.Status().Update(ctx, fg); err != nil {
		return ctrl.Result{}, fmt.Errorf("set ready condition: %w", err)
	}

	return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
}

// setFailedCondition sets a Failed-type condition and returns an error result.
func (r *FoundryGraphReconciler) setFailedCondition(ctx context.Context, fg *flowv1.FoundryGraph, reconcileErr error) (ctrl.Result, error) {
	// Re-fetch to get latest resourceVersion.
	if err := r.Get(ctx, client.ObjectKeyFromObject(fg), fg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// A ReconcileFailed condition represents an ordinary (non-OPEN-on-transactions)
	// failure; the blocked-destructive-change class funnels exclusively to
	// setBlockedCondition. Clear any stale DestructiveChangeBlocked so a previously
	// blocked FoundryGraph that later fails for an unrelated reason does not retain a
	// blocked condition that no longer describes the current reconcile (SPEC R6:
	// blocking conditions belong only to the WipeGraph open-transaction error class).
	// Clear any stale FoundryGraphConflict too: reaching a failure here means this
	// resource passed singleton enforcement (it is the owner), so a conflict condition
	// from a prior conflicting state is stale (SPEC R1).
	meta.RemoveStatusCondition(&fg.Status.Conditions, "DestructiveChangeBlocked")
	meta.RemoveStatusCondition(&fg.Status.Conditions, "FoundryGraphConflict")

	meta.SetStatusCondition(&fg.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		ObservedGeneration: fg.Generation,
		Reason:             reasonReconcileFailed,
		Message:            reconcileErr.Error(),
	})

	if err := r.Status().Update(ctx, fg); err != nil {
		return ctrl.Result{}, fmt.Errorf("set failed condition: %w", err)
	}

	return ctrl.Result{}, reconcileErr
}

// setBlockedCondition sets a DestructiveChangeBlocked condition.
func (r *FoundryGraphReconciler) setBlockedCondition(ctx context.Context, fg *flowv1.FoundryGraph, reconcileErr error) (ctrl.Result, error) {
	if err := r.Get(ctx, client.ObjectKeyFromObject(fg), fg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// ponytail: a blocked destructive change sets only DestructiveChangeBlocked=True and
	// leaves Ready untouched. After a previously-Ready=True resource hits a blocked change,
	// it therefore carries Ready=True alongside DestructiveChangeBlocked=True, which is
	// inconsistent for aggregate consumers that collapse Ready. This is intentional: the
	// block is a transient operator wait (spec is unchanged, the wipe is simply deferred
	// until open transactions finish), not a Ready-worthy failure, and the spec only mandates
	// the DestructiveChangeBlocked condition. A fixed consumer that requires Ready=False
	// here should treat either DestructiveChangeBlocked=True, Ready=True as "not applying".
	// A stale FoundryGraphConflict is cleared: reaching the blocked path means this
	// resource is the singleton owner being provisioned (SPEC R1).
	meta.RemoveStatusCondition(&fg.Status.Conditions, "FoundryGraphConflict")
	meta.SetStatusCondition(&fg.Status.Conditions, metav1.Condition{
		Type:               "DestructiveChangeBlocked",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: fg.Generation,
		Reason:             "WipeGraphFailed",
		Message:            reconcileErr.Error(),
	})

	if err := r.Status().Update(ctx, fg); err != nil {
		return ctrl.Result{}, fmt.Errorf("set blocked condition: %w", err)
	}

	return ctrl.Result{}, reconcileErr
}

// SetupWithManager sets up the controller with the Manager.
func (r *FoundryGraphReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&flowv1.FoundryGraph{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&corev1.Secret{}).
		Named("foundrygraph").
		// SPEC R6 steps 5-6 and the destructive-change flow require error requeues to
		// use the documented exponential backoff (initial ~5s, doubling, 5m cap) rather
		// than controller-runtime's default per-item limiter (5ms initial, 1000s cap).
		WithOptions(controller.Options{RateLimiter: readinessRateLimiter()}).
		Complete(r)
}
