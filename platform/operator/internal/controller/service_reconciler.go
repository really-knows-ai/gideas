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

type ServiceObject interface {
	client.Object
	GetSpecImage() string
	GetSpecMinReplicas() *int32
	GetSpecDeploymentStrategy() string
	GetSpecResources() *corev1.ResourceRequirements
	GetSpecStorage() *flowv1.StorageConfig
}

type ServiceWithOutput interface {
	GetSpecOutputFormat() string
}

type statusUpdater interface {
	GetPhase() string
	SetPhase(string)
	SetAvailableReplicas(int32)
	GetConditions() []metav1.Condition
	SetConditions([]metav1.Condition)
}

type ServiceReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	ContainerName string
	AppLabelName  string
	LabelKey      string
	TypeName      string
}

func (r *ServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request, newObj func() ServiceObject) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	svc := newObj()
	if err := r.Get(ctx, req.NamespacedName, svc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Reconciling "+r.TypeName,
		"name", svc.GetName(),
		"namespace", svc.GetNamespace(),
	)

	if p, ok := svc.(statusUpdater); ok && p.GetPhase() == "" {
		p.SetPhase(phaseInitialising)
	}

	useStatefulSet := svc.GetSpecDeploymentStrategy() == strategyStatefulSet

	var availableReplicas int32
	var reconcileErr error
	if useStatefulSet {
		availableReplicas, reconcileErr = r.reconcileStatefulSet(ctx, svc)
	} else {
		availableReplicas, reconcileErr = r.reconcileDeployment(ctx, svc)
	}
	if reconcileErr != nil {
		return r.persistStatus(ctx, svc, phaseDegraded, 0, conditionReady,
			metav1.ConditionFalse, "ReconcileFailed", reconcileErr.Error(), newObj)
	}

	phase := r.determinePhase(svc, availableReplicas)

	condStatus := metav1.ConditionTrue
	reason := "Reconciled"
	message := fmt.Sprintf("%s reconciled with %d available replicas", r.TypeName, availableReplicas)
	if phase != phaseReady {
		condStatus = metav1.ConditionFalse
		reason = "NotReady"
		message = fmt.Sprintf("Phase is %s with %d replicas", phase, availableReplicas)
	}
	return r.persistStatus(ctx, svc, phase, availableReplicas, conditionReady, condStatus, reason, message, newObj)
}

func (r *ServiceReconciler) determinePhase(svc ServiceObject, available int32) string {
	minReplicas := int32(0)
	if mr := svc.GetSpecMinReplicas(); mr != nil {
		minReplicas = *mr
	}
	if minReplicas == 0 && available == 0 {
		return phaseStopped
	}
	if available > 0 {
		return phaseReady
	}
	return phaseInitialising
}

func (r *ServiceReconciler) reconcileDeployment(ctx context.Context, svc ServiceObject) (int32, error) {
	replicas := int32(1)
	if mr := svc.GetSpecMinReplicas(); mr != nil && *mr > 0 {
		replicas = *mr
	}

	labels := r.labelsForService(svc)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svc.GetName(),
			Namespace: svc.GetNamespace(),
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

func (r *ServiceReconciler) reconcileStatefulSet(ctx context.Context, svc ServiceObject) (int32, error) {
	replicas := int32(1)
	if mr := svc.GetSpecMinReplicas(); mr != nil && *mr > 0 {
		replicas = *mr
	}

	labels := r.labelsForService(svc)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svc.GetName(),
			Namespace: svc.GetNamespace(),
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sts, func() error {
		sts.Labels = labels
		sts.Spec.Replicas = &replicas
		sts.Spec.ServiceName = svc.GetName()
		sts.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		sts.Spec.Template = r.buildPodTemplate(svc, labels)

		if storage := svc.GetSpecStorage(); storage != nil {
			var pvcs []corev1.PersistentVolumeClaim
			for _, vol := range storage.Volumes {
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

func (r *ServiceReconciler) buildPodTemplate(svc ServiceObject, labels map[string]string) corev1.PodTemplateSpec {
	container := corev1.Container{
		Name:  r.ContainerName,
		Image: svc.GetSpecImage(),
	}

	if sv, ok := svc.(ServiceWithOutput); ok {
		container.Env = []corev1.EnvVar{
			{Name: "OUTPUT_FORMAT", Value: sv.GetSpecOutputFormat()},
		}
	}

	if res := svc.GetSpecResources(); res != nil {
		container.Resources = *res
	}

	if storage := svc.GetSpecStorage(); storage != nil {
		for _, vol := range storage.Volumes {
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

func (r *ServiceReconciler) labelsForService(svc ServiceObject) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       r.AppLabelName,
		"app.kubernetes.io/instance":   svc.GetName(),
		"app.kubernetes.io/managed-by": "foundry-operator",
		r.LabelKey:                     svc.GetName(),
	}
}

func (r *ServiceReconciler) persistStatus(
	ctx context.Context,
	svc ServiceObject,
	phase string,
	availableReplicas int32,
	condType string,
	condStatus metav1.ConditionStatus,
	reason, message string,
	newObj func() ServiceObject,
) (ctrl.Result, error) {
	fresh := newObj()
	if err := r.Get(ctx, client.ObjectKeyFromObject(svc), fresh); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	updater, ok := fresh.(statusUpdater)
	if !ok {
		return ctrl.Result{}, fmt.Errorf("%T does not implement statusUpdater", fresh)
	}
	updater.SetPhase(phase)
	updater.SetAvailableReplicas(availableReplicas)
	conds := updater.GetConditions()
	meta.SetStatusCondition(&conds, metav1.Condition{
		Type:               condType,
		Status:             condStatus,
		ObservedGeneration: svc.GetGeneration(),
		Reason:             reason,
		Message:            message,
	})
	updater.SetConditions(conds)

	if err := r.Status().Update(ctx, fresh); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}
