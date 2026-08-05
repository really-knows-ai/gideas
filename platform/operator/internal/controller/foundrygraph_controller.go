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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

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

// grpcCallTimeout bounds each Cartographer RPC phase issued by the reconciler. The
// controller-runtime reconcile ctx carries no per-reconcile deadline (only manager
// cancellation), so a slow or blackholed Cartographer would otherwise hang the reconcile
// indefinitely rather than failing fast into the SPEC R6 requeue-with-backoff path.
const grpcCallTimeout = 30 * time.Second

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

	// Reject duplicate type names — SPEC requires duplicates within entityTypes/edgeTypes
	// to fail schema application (INVALID_ARGUMENT). The operator-side diff would otherwise
	// silently deduplicate them (last wins), so reject before diffing.
	if dup := schemaDuplicateNames(&fg.Spec); dup != "" {
		return r.setFailedCondition(ctx, &fg, fmt.Errorf("invalid schema: %s", dup))
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
			return r.setFailedCondition(ctx, &fg, err)
		}
	case SchemaDiffNonDestructive:
		// Non-destructive: HealthCheck -> ApplySchema on existing pod.
		if err := r.applySchemaOnExisting(ctx, &fg, false); err != nil {
			return r.setFailedCondition(ctx, &fg, err)
		}
	}

	// Steps 4-8: Reconcile infrastructure.
	if err := r.reconcilePVC(ctx, &fg); err != nil {
		return r.setFailedCondition(ctx, &fg, err)
	}
	if err := r.reconcileSecrets(ctx, &fg); err != nil {
		return r.setFailedCondition(ctx, &fg, err)
	}
	if err := r.reconcileRBAC(ctx, &fg); err != nil {
		return r.setFailedCondition(ctx, &fg, err)
	}
	if err := r.reconcileDeployment(ctx, &fg); err != nil {
		return r.setFailedCondition(ctx, &fg, err)
	}
	if err := r.reconcileService(ctx, &fg); err != nil {
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
	if err := r.applySchema(ctx, &fg); err != nil {
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

// applySchemaOnExisting applies schema changes to the existing Cartographer pod.
// If destructive is true, calls WipeGraph first.
func (r *FoundryGraphReconciler) applySchemaOnExisting(ctx context.Context, fg *flowv1.FoundryGraph, destructive bool) error {
	// Dial the existing cartographer pod.
	endpoint := fmt.Sprintf("%s.%s.svc.cluster.local:%d", r.cartographerServiceName(fg), fg.Namespace, r.CartographerPort)
	client, err := r.CartographerDialer(ctx, endpoint)
	if err != nil {
		return fmt.Errorf("dial existing cartographer: %w", err)
	}
	defer client.Close()

	// Bound the RPC phase with a per-call deadline: the reconcile ctx has no deadline (only
	// manager cancellation), so without this a blackholed Cartographer hangs the reconcile
	// instead of failing fast into the SPEC R6 requeue-with-backoff path.
	rpcCtx, rpcCancel := context.WithTimeout(ctx, grpcCallTimeout)
	defer rpcCancel()

	// HealthCheck
	if _, err := client.HealthCheck(rpcCtx, &flowv1gen.HealthCheckRequest{}); err != nil {
		return fmt.Errorf("health check on existing pod: %w", err)
	}

	if destructive {
		// WipeGraph
		if _, err := client.WipeGraph(rpcCtx, &flowv1gen.WipeGraphRequest{}); err != nil {
			if isFailedPrecondition(err) {
				// DISTINCT SENTINEL: only this case (WipeGraph blocked by open
				// transactions) deserves the DestructiveChangeBlocked condition.
				return fmt.Errorf("%w: %v", errWipeBlockedByOpenTransactions, err)
			}
			return fmt.Errorf("wipe graph: %w", err)
		}
	}

	// ApplySchema
	schema := r.schemaFromCRD(&fg.Spec)
	if _, err := client.ApplySchema(rpcCtx, &flowv1gen.ApplySchemaRequest{Schema: schema}); err != nil {
		return fmt.Errorf("apply schema on existing pod: %w", err)
	}

	return nil
}

// applySchema applies the schema to a (newly-ready) Cartographer pod.
func (r *FoundryGraphReconciler) applySchema(ctx context.Context, fg *flowv1.FoundryGraph) error {
	// Re-fetch the CR to get the latest spec.
	if err := r.Get(ctx, client.ObjectKeyFromObject(fg), fg); err != nil {
		return client.IgnoreNotFound(err)
	}

	endpoint := fmt.Sprintf("%s.%s.svc.cluster.local:%d", r.cartographerServiceName(fg), fg.Namespace, r.CartographerPort)
	c, err := r.CartographerDialer(ctx, endpoint)
	if err != nil {
		return fmt.Errorf("dial cartographer: %w", err)
	}
	defer c.Close()

	// Bound the RPC phase with a per-call deadline (the reconcile ctx has no deadline).
	rpcCtx, rpcCancel := context.WithTimeout(ctx, grpcCallTimeout)
	defer rpcCancel()

	// HealthCheck
	if _, err := c.HealthCheck(rpcCtx, &flowv1gen.HealthCheckRequest{}); err != nil {
		return fmt.Errorf("health check: %w", err)
	}

	// ApplySchema
	schema := r.schemaFromCRD(&fg.Spec)
	if _, err := c.ApplySchema(rpcCtx, &flowv1gen.ApplySchemaRequest{Schema: schema}); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}

	return nil
}

// isFailedPrecondition checks if a gRPC error is FAILED_PRECONDITION.
func isFailedPrecondition(err error) bool {
	return status.Code(err) == codes.FailedPrecondition
}

// waitForReadiness polls the Deployment until it is ready or the timeout elapses. The
// readiness timeout is the sole termination condition: transient Get errors (e.g. a
// Deployment momentarily not yet visible after CreateOrUpdate) do not short-circuit the poll.
func (r *FoundryGraphReconciler) waitForReadiness(ctx context.Context, fg *flowv1.FoundryGraph) error {
	log := logf.FromContext(ctx)
	deployName := "cartographer-" + fg.Name
	nn := types.NamespacedName{Name: deployName, Namespace: fg.Namespace}

	deadline := time.Now().Add(r.ReadinessTimeout)
	pollInterval := 5 * time.Second

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

// allReplicasReady reports whether every desired replica is ready (SPEC: "all replicas
// ready, readiness probe passing"). Using Replicas/ReadyReplicas rather than
// AvailableReplicas > 0 keeps the check correct even if the replica count ever diverges
// from the hardcoded value in the Deployment.
func allReplicasReady(deploy *appsv1.Deployment) bool {
	if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas == 0 {
		return false
	}
	return deploy.Status.ReadyReplicas >= *deploy.Spec.Replicas
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
	specJSON, err := json.Marshal(currentSpec)
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
	// destructive-change attempt that has since recovered.
	meta.RemoveStatusCondition(&fg.Status.Conditions, "DestructiveChangeBlocked")
	meta.SetStatusCondition(&fg.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: fg.Generation,
		Reason:             "Reconciled",
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
	meta.RemoveStatusCondition(&fg.Status.Conditions, "DestructiveChangeBlocked")

	meta.SetStatusCondition(&fg.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		ObservedGeneration: fg.Generation,
		Reason:             "ReconcileFailed",
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
		Complete(r)
}
