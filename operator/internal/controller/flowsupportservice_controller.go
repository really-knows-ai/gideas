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
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	flowv1 "github.com/gideas/flow/operator/api/v1"
)

// FlowSupportServiceReconciler reconciles a FlowSupportService object.
//
// Responsibilities:
//   - Deploy and manage pods (ReplicaSet or StatefulSet) based on deployment strategy.
//   - Track phase (Initialising, Ready, Degraded, Stopped) and available replicas.
//   - Set status conditions reflecting reconciliation health.
type FlowSupportServiceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=flow.gideas.io,resources=flowsupportservices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=flow.gideas.io,resources=flowsupportservices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=flow.gideas.io,resources=flowsupportservices/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete

// Reconcile creates or updates the workload for the FlowSupportService
// and tracks its phase and available replicas.
func (r *FlowSupportServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the FlowSupportService instance.
	var svc flowv1.FlowSupportService
	if err := r.Get(ctx, req.NamespacedName, &svc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Reconciling FlowSupportService",
		"name", svc.Name,
		"namespace", svc.Namespace,
	)

	// Set initial phase if empty.
	if svc.Status.Phase == "" {
		svc.Status.Phase = phaseInitialising
	}

	useStatefulSet := svc.Spec.DeploymentStrategy == strategyStatefulSet

	// Reconcile the workload.
	var availableReplicas int32
	var reconcileErr error

	if useStatefulSet {
		availableReplicas, reconcileErr = r.reconcileStatefulSet(ctx, &svc)
	} else {
		availableReplicas, reconcileErr = r.reconcileDeployment(ctx, &svc)
	}

	if reconcileErr != nil {
		return r.persistStatus(ctx, &svc, phaseDegraded, 0, conditionReady,
			metav1.ConditionFalse, "ReconcileFailed", reconcileErr.Error())
	}

	// Determine phase from replica count.
	phase := r.determinePhase(&svc, availableReplicas)

	condStatus := metav1.ConditionTrue
	reason := "Reconciled"
	message := fmt.Sprintf("FlowSupportService reconciled with %d available replicas", availableReplicas)
	if phase != phaseReady {
		condStatus = metav1.ConditionFalse
		reason = "NotReady"
		message = fmt.Sprintf("Phase is %s with %d replicas", phase, availableReplicas)
	}

	return r.persistStatus(ctx, &svc, phase, availableReplicas, conditionReady, condStatus, reason, message)
}

// determinePhase computes the phase based on replica availability.
func (r *FlowSupportServiceReconciler) determinePhase(svc *flowv1.FlowSupportService, available int32) string {
	minReplicas := int32(0)
	if svc.Spec.MinReplicas != nil {
		minReplicas = *svc.Spec.MinReplicas
	}

	if minReplicas == 0 && available == 0 {
		return phaseStopped
	}
	if available > 0 {
		return phaseReady
	}
	return phaseInitialising
}

// reconcileDeployment creates or updates a Deployment for the FlowSupportService.
func (r *FlowSupportServiceReconciler) reconcileDeployment(ctx context.Context, svc *flowv1.FlowSupportService) (int32, error) {
	replicas := int32(1)
	if svc.Spec.MinReplicas != nil && *svc.Spec.MinReplicas > 0 {
		replicas = *svc.Spec.MinReplicas
	}

	labels := r.labelsForService(svc)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svc.Name,
			Namespace: svc.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		deploy.Labels = labels
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deploy.Spec.Template = r.buildPodTemplate(svc, labels)
		return controllerutil.SetControllerReference(svc, deploy, r.Scheme)
	})
	if err != nil {
		return 0, fmt.Errorf("could not reconcile Deployment: %w", err)
	}

	return deploy.Status.AvailableReplicas, nil
}

// reconcileStatefulSet creates or updates a StatefulSet for the FlowSupportService.
func (r *FlowSupportServiceReconciler) reconcileStatefulSet(ctx context.Context, svc *flowv1.FlowSupportService) (int32, error) {
	replicas := int32(1)
	if svc.Spec.MinReplicas != nil && *svc.Spec.MinReplicas > 0 {
		replicas = *svc.Spec.MinReplicas
	}

	labels := r.labelsForService(svc)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svc.Name,
			Namespace: svc.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sts, func() error {
		sts.Labels = labels
		sts.Spec.Replicas = &replicas
		sts.Spec.ServiceName = svc.Name
		sts.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		sts.Spec.Template = r.buildPodTemplate(svc, labels)

		// Build VolumeClaimTemplates from storage config.
		if svc.Spec.Storage != nil {
			var pvcs []corev1.PersistentVolumeClaim
			for _, vol := range svc.Spec.Storage.Volumes {
				storageSize := defaultStorageSize
				if vol.Size != "" {
					storageSize = vol.Size
				}
				pvcs = append(pvcs, corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{Name: vol.Name},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse(storageSize),
							},
						},
					},
				})
			}
			sts.Spec.VolumeClaimTemplates = pvcs
		}

		return controllerutil.SetControllerReference(svc, sts, r.Scheme)
	})
	if err != nil {
		return 0, fmt.Errorf("could not reconcile StatefulSet: %w", err)
	}

	return sts.Status.ReadyReplicas, nil
}

// buildPodTemplate constructs the pod template spec for the support service.
func (r *FlowSupportServiceReconciler) buildPodTemplate(svc *flowv1.FlowSupportService, labels map[string]string) corev1.PodTemplateSpec {
	container := corev1.Container{
		Name:  "support-service",
		Image: svc.Spec.Image,
	}

	if svc.Spec.Resources != nil {
		container.Resources = *svc.Spec.Resources
	}

	// Inject volume mounts from storage config.
	if svc.Spec.Storage != nil {
		for _, vol := range svc.Spec.Storage.Volumes {
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      vol.Name,
				MountPath: vol.MountPath,
			})
		}
	}

	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: labels},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{container},
		},
	}
}

// persistStatus re-fetches and persists the FlowSupportService status.
func (r *FlowSupportServiceReconciler) persistStatus(
	ctx context.Context,
	svc *flowv1.FlowSupportService,
	phase string,
	availableReplicas int32,
	condType string,
	condStatus metav1.ConditionStatus,
	reason, message string,
) (ctrl.Result, error) {
	var fresh flowv1.FlowSupportService
	if err := r.Get(ctx, client.ObjectKeyFromObject(svc), &fresh); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	fresh.Status.Phase = phase
	fresh.Status.AvailableReplicas = availableReplicas
	meta.SetStatusCondition(&fresh.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             condStatus,
		ObservedGeneration: svc.Generation,
		Reason:             reason,
		Message:            message,
	})

	if err := r.Status().Update(ctx, &fresh); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// labelsForService returns standard labels for resources owned by this FlowSupportService.
func (r *FlowSupportServiceReconciler) labelsForService(svc *flowv1.FlowSupportService) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "flowsupportservice",
		"app.kubernetes.io/instance":   svc.Name,
		"app.kubernetes.io/managed-by": "foundry-operator",
		"flow.gideas.io/support":       svc.Name,
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *FlowSupportServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&flowv1.FlowSupportService{}).
		Owns(&appsv1.Deployment{}).
		Owns(&appsv1.StatefulSet{}).
		Named("flowsupportservice").
		Complete(r)
}
