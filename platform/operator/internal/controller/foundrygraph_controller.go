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
			if diffResult == SchemaDiffNone {
				// Non-schema field changed; set diffResult to indicate no schema change.
				// We use a special internal detection: since diffSchema returned None but
				// semantic equality says they differ, it's a non-schema change.
				diffResult = SchemaDiffNone
			}
		}
	}

	// Build the spec map for comparison at reconcile start.
	currentSpec := *fg.Spec.DeepCopy()

	// Branching logic for spec changes (see PHASE_05.md D1 branching pseudocode).
	switch diffResult {
	case SchemaDiffDestructive:
		// Destructive: HealthCheck -> WipeGraph -> ApplySchema on existing pod.
		if err := r.applySchemaOnExisting(ctx, &fg, true); err != nil {
			return r.setBlockedCondition(ctx, &fg, err)
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
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, fmt.Errorf("readiness: %w", err)
	}

	// Step 10: ApplySchema on new pod if spec changed.
	if diffResult != SchemaDiffNone {
		if err := r.applySchema(ctx, &fg); err != nil {
			return r.setFailedCondition(ctx, &fg, err)
		}
	} else if oldSpec == nil {
		// First reconcile — always apply schema.
		if err := r.applySchema(ctx, &fg); err != nil {
			return r.setFailedCondition(ctx, &fg, err)
		}
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

	// HealthCheck
	if _, err := client.HealthCheck(ctx, &flowv1gen.HealthCheckRequest{}); err != nil {
		return fmt.Errorf("health check on existing pod: %w", err)
	}

	if destructive {
		// WipeGraph
		if _, err := client.WipeGraph(ctx, &flowv1gen.WipeGraphRequest{}); err != nil {
			if isFailedPrecondition(err) {
				return fmt.Errorf("wipe blocked by open transactions: %w", err)
			}
			return fmt.Errorf("wipe graph: %w", err)
		}
	}

	// ApplySchema
	schema := r.schemaFromCRD(&fg.Spec)
	if _, err := client.ApplySchema(ctx, &flowv1gen.ApplySchemaRequest{Schema: schema}); err != nil {
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

	// HealthCheck
	if _, err := c.HealthCheck(ctx, &flowv1gen.HealthCheckRequest{}); err != nil {
		return fmt.Errorf("health check: %w", err)
	}

	// ApplySchema
	schema := r.schemaFromCRD(&fg.Spec)
	if _, err := c.ApplySchema(ctx, &flowv1gen.ApplySchemaRequest{Schema: schema}); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}

	return nil
}

// isFailedPrecondition checks if a gRPC error is FAILED_PRECONDITION.
func isFailedPrecondition(err error) bool {
	return status.Code(err) == codes.FailedPrecondition
}

// waitForReadiness polls the Deployment until it is ready or the timeout elapses.
func (r *FoundryGraphReconciler) waitForReadiness(ctx context.Context, fg *flowv1.FoundryGraph) error {
	log := logf.FromContext(ctx)
	deployName := "cartographer-" + fg.Name
	nn := types.NamespacedName{Name: deployName, Namespace: fg.Namespace}

	deadline := time.Now().Add(r.ReadinessTimeout)
	pollInterval := 5 * time.Second

	for time.Now().Before(deadline) {
		var deploy appsv1.Deployment
		if err := r.Get(ctx, nn, &deploy); err != nil {
			return fmt.Errorf("get deployment: %w", err)
		}
		if deploy.Status.AvailableReplicas > 0 {
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

// updateStatus sets the endpoint, storageSize, and last-applied-spec annotation.
func (r *FoundryGraphReconciler) updateStatus(ctx context.Context, fg *flowv1.FoundryGraph, currentSpec *flowv1.FoundryGraphSpec) error {
	// Re-fetch to get latest resourceVersion.
	if err := r.Get(ctx, client.ObjectKeyFromObject(fg), fg); err != nil {
		return client.IgnoreNotFound(err)
	}

	// Set endpoint.
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

	// Store the full serialized current spec in annotation.
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
