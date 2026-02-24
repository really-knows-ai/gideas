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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	flowv1 "github.com/gideas/flow/operator/api/v1"
)

// Control Plane infrastructure images.
const (
	eventBusImage        = "ghcr.io/gideas/flow/eventbus:latest"
	frictionLedgerImage  = "ghcr.io/gideas/flow/frictionledger:latest"
	flowMonitorImage     = "ghcr.io/gideas/flow/monitor:latest"
	librarianImage       = "ghcr.io/gideas/flow/librarian:latest"
	eventBusPort         = 50056
	frictionLedgerPort   = 50057
	librarianPort        = 50058
	flowMonitorHTTPPort  = 2112
	infraStorageSize     = "1Gi"
	monitorStorageSize   = "100Mi"
	eventBusServiceName  = "flow-eventbus"
	frictionLedgerSvcNm  = "flow-frictionledger"
	flowMonitorSvcName   = "flow-monitor"
	librarianSvcName     = "flow-librarian"
	eventBusDBPath       = "/data/eventbus.db"
	frictionLedgerDBPath = "/data/frictionledger.db"
	librarianDBPath      = "/data/librarian.db"
	monitorCheckpointPth = "/data/monitor-checkpoint.json"
	operatorSvcName      = "flow-operator"
	operatorPort         = 50052
)

// reconcileInfrastructure ensures that the Event Bus, Friction Ledger, and Flow Monitor
// Deployments and Services exist in the Flow's namespace. This is called during every
// reconcile after validation succeeds.
func (r *FoundryFlowReconciler) reconcileInfrastructure(ctx context.Context, flow *flowv1.FoundryFlow) error {
	log := logf.FromContext(ctx)
	log.Info("Reconciling control-plane infrastructure",
		"namespace", flow.Namespace,
	)

	if err := r.reconcileEventBus(ctx, flow); err != nil {
		return fmt.Errorf("could not reconcile Event Bus: %w", err)
	}

	if err := r.reconcileFrictionLedger(ctx, flow); err != nil {
		return fmt.Errorf("could not reconcile Friction Ledger: %w", err)
	}

	if err := r.reconcileFlowMonitor(ctx, flow); err != nil {
		return fmt.Errorf("could not reconcile Flow Monitor: %w", err)
	}

	if err := r.reconcileLibrarian(ctx, flow); err != nil {
		return fmt.Errorf("could not reconcile Librarian: %w", err)
	}

	return nil
}

// -----------------------------------------------------------------------
// Event Bus
// -----------------------------------------------------------------------

func (r *FoundryFlowReconciler) reconcileEventBus(ctx context.Context, flow *flowv1.FoundryFlow) error {
	if err := r.reconcileEventBusDeployment(ctx, flow); err != nil {
		return err
	}
	return r.reconcileService(ctx, flow, eventBusServiceName, eventBusPort, "grpc")
}

func (r *FoundryFlowReconciler) reconcileEventBusDeployment(ctx context.Context, flow *flowv1.FoundryFlow) error {
	replicas := int32(1)
	labels := infraLabels(eventBusServiceName)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      eventBusServiceName,
			Namespace: flow.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		deploy.Labels = labels
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deploy.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:            "eventbus",
					Image:           eventBusImage,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Ports: []corev1.ContainerPort{{
						Name:          "grpc",
						ContainerPort: int32(eventBusPort),
						Protocol:      corev1.ProtocolTCP,
					}},
					Env:          r.eventBusEnvVars(flow),
					VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
				}},
				Volumes: []corev1.Volume{{
					Name: "data",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				}},
			},
		}
		return controllerutil.SetControllerReference(flow, deploy, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("could not reconcile Event Bus Deployment: %w", err)
	}

	logf.FromContext(ctx).Info("Reconciled Event Bus Deployment",
		"name", deploy.Name, "result", result,
	)
	return nil
}

// eventBusEnvVars builds the Event Bus env var list from the FoundryFlow spec.
func (r *FoundryFlowReconciler) eventBusEnvVars(flow *flowv1.FoundryFlow) []corev1.EnvVar {
	envs := []corev1.EnvVar{
		{Name: "EVENT_BUS_PORT", Value: fmt.Sprintf("%d", eventBusPort)},
		{Name: "EVENT_BUS_DB_PATH", Value: eventBusDBPath},
	}

	if flow.Spec.EventBusConfig == nil {
		return envs
	}
	ret := flow.Spec.EventBusConfig.Retention

	if len(ret) > 0 {
		data, err := json.Marshal(ret)
		if err == nil {
			envs = append(envs, corev1.EnvVar{
				Name:  "EVENT_BUS_RETENTION_CONFIG",
				Value: string(data),
			})
		}
	}

	return envs
}

// -----------------------------------------------------------------------
// Friction Ledger
// -----------------------------------------------------------------------

func (r *FoundryFlowReconciler) reconcileFrictionLedger(ctx context.Context, flow *flowv1.FoundryFlow) error {
	if err := r.reconcileFrictionLedgerDeployment(ctx, flow); err != nil {
		return err
	}
	return r.reconcileService(ctx, flow, frictionLedgerSvcNm, frictionLedgerPort, "grpc")
}

func (r *FoundryFlowReconciler) reconcileFrictionLedgerDeployment(ctx context.Context, flow *flowv1.FoundryFlow) error {
	replicas := int32(1)
	labels := infraLabels(frictionLedgerSvcNm)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      frictionLedgerSvcNm,
			Namespace: flow.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		deploy.Labels = labels
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deploy.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:            "frictionledger",
					Image:           frictionLedgerImage,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Ports: []corev1.ContainerPort{{
						Name:          "grpc",
						ContainerPort: int32(frictionLedgerPort),
						Protocol:      corev1.ProtocolTCP,
					}},
					Env:          r.frictionLedgerEnvVars(flow),
					VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
				}},
				Volumes: []corev1.Volume{{
					Name: "data",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				}},
			},
		}
		return controllerutil.SetControllerReference(flow, deploy, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("could not reconcile Friction Ledger Deployment: %w", err)
	}

	logf.FromContext(ctx).Info("Reconciled Friction Ledger Deployment",
		"name", deploy.Name, "result", result,
	)
	return nil
}

// frictionLedgerEnvVars builds the Friction Ledger env var list, including
// the Event Bus address and per-tier friction thresholds.
func (r *FoundryFlowReconciler) frictionLedgerEnvVars(flow *flowv1.FoundryFlow) []corev1.EnvVar {
	envs := []corev1.EnvVar{
		{Name: "FRICTION_LEDGER_PORT", Value: fmt.Sprintf("%d", frictionLedgerPort)},
		{Name: "FRICTION_LEDGER_DB_PATH", Value: frictionLedgerDBPath},
		{Name: "EVENT_BUS_ADDRESS", Value: fmt.Sprintf("%s:%d", eventBusServiceName, eventBusPort)},
	}

	ft := flow.Spec.GovernancePolicy.FrictionThresholds
	if ft == nil {
		return envs
	}

	thresholdEnv := func(key string, val *resource.Quantity) {
		if val != nil {
			envs = append(envs, corev1.EnvVar{Name: key, Value: val.AsDec().String()})
		}
	}

	thresholdEnv("FRICTION_THRESHOLD_TIER1", ft.Tier1)
	thresholdEnv("FRICTION_THRESHOLD_TIER2", ft.Tier2)
	thresholdEnv("FRICTION_THRESHOLD_TIER3", ft.Tier3)
	thresholdEnv("FRICTION_THRESHOLD_TIER4", ft.Tier4)
	thresholdEnv("FRICTION_THRESHOLD_TIER5", ft.Tier5)

	return envs
}

// -----------------------------------------------------------------------
// Flow Monitor
// -----------------------------------------------------------------------

func (r *FoundryFlowReconciler) reconcileFlowMonitor(ctx context.Context, flow *flowv1.FoundryFlow) error {
	if err := r.reconcileFlowMonitorDeployment(ctx, flow); err != nil {
		return err
	}
	return r.reconcileService(ctx, flow, flowMonitorSvcName, flowMonitorHTTPPort, "http-metrics")
}

func (r *FoundryFlowReconciler) reconcileFlowMonitorDeployment(ctx context.Context, flow *flowv1.FoundryFlow) error {
	replicas := int32(1)
	labels := infraLabels(flowMonitorSvcName)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      flowMonitorSvcName,
			Namespace: flow.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		deploy.Labels = labels
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deploy.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:            "monitor",
					Image:           flowMonitorImage,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Ports: []corev1.ContainerPort{{
						Name:          "http-metrics",
						ContainerPort: int32(flowMonitorHTTPPort),
						Protocol:      corev1.ProtocolTCP,
					}},
					Env: []corev1.EnvVar{
						{Name: "FLOW_MONITOR_PORT", Value: fmt.Sprintf("%d", flowMonitorHTTPPort)},
						{Name: "FLOW_MONITOR_CHECKPOINT_PATH", Value: monitorCheckpointPth},
						{Name: "EVENT_BUS_ADDRESS", Value: fmt.Sprintf("%s:%d", eventBusServiceName, eventBusPort)},
					},
					VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
				}},
				Volumes: []corev1.Volume{{
					Name: "data",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				}},
			},
		}
		return controllerutil.SetControllerReference(flow, deploy, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("could not reconcile Flow Monitor Deployment: %w", err)
	}

	logf.FromContext(ctx).Info("Reconciled Flow Monitor Deployment",
		"name", deploy.Name, "result", result,
	)
	return nil
}

// -----------------------------------------------------------------------
// Librarian
// -----------------------------------------------------------------------

func (r *FoundryFlowReconciler) reconcileLibrarian(ctx context.Context, flow *flowv1.FoundryFlow) error {
	if err := r.reconcileLibrarianDeployment(ctx, flow); err != nil {
		return err
	}
	return r.reconcileService(ctx, flow, librarianSvcName, librarianPort, "grpc")
}

func (r *FoundryFlowReconciler) reconcileLibrarianDeployment(ctx context.Context, flow *flowv1.FoundryFlow) error {
	replicas := int32(1)
	labels := infraLabels(librarianSvcName)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      librarianSvcName,
			Namespace: flow.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		deploy.Labels = labels
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deploy.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:            "librarian",
					Image:           librarianImage,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Ports: []corev1.ContainerPort{{
						Name:          "grpc",
						ContainerPort: int32(librarianPort),
						Protocol:      corev1.ProtocolTCP,
					}},
					Env:          r.librarianEnvVars(flow),
					VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
				}},
				Volumes: []corev1.Volume{{
					Name: "data",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				}},
			},
		}
		return controllerutil.SetControllerReference(flow, deploy, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("could not reconcile Librarian Deployment: %w", err)
	}

	logf.FromContext(ctx).Info("Reconciled Librarian Deployment",
		"name", deploy.Name, "result", result,
	)
	return nil
}

// librarianEnvVars builds the Librarian env var list, including Event Bus address,
// Operator address, and per-tier review TTLs.
func (r *FoundryFlowReconciler) librarianEnvVars(flow *flowv1.FoundryFlow) []corev1.EnvVar {
	envs := []corev1.EnvVar{
		{Name: "LIBRARIAN_PORT", Value: fmt.Sprintf("%d", librarianPort)},
		{Name: "LIBRARIAN_DB_PATH", Value: librarianDBPath},
		{Name: "EVENT_BUS_ADDRESS", Value: fmt.Sprintf("%s:%d", eventBusServiceName, eventBusPort)},
		{Name: "OPERATOR_ADDRESS", Value: fmt.Sprintf("%s:%d", operatorSvcName, operatorPort)},
	}

	ttls := flow.Spec.GovernancePolicy.ReviewTTLs
	if ttls == nil {
		return envs
	}

	ttlEnv := func(key string, val *metav1.Duration) {
		if val != nil {
			envs = append(envs, corev1.EnvVar{Name: key, Value: val.Duration.String()})
		}
	}

	ttlEnv("REVIEW_TTL_TIER1", ttls.Tier1)
	ttlEnv("REVIEW_TTL_TIER2", ttls.Tier2)
	ttlEnv("REVIEW_TTL_TIER3", ttls.Tier3)
	ttlEnv("REVIEW_TTL_TIER4", ttls.Tier4)
	ttlEnv("REVIEW_TTL_TIER5", ttls.Tier5)

	return envs
}

// -----------------------------------------------------------------------
// Shared: Service reconciliation
// -----------------------------------------------------------------------

// reconcileService creates or updates a ClusterIP Service for the given name and port.
func (r *FoundryFlowReconciler) reconcileService(
	ctx context.Context,
	flow *flowv1.FoundryFlow,
	name string,
	port int,
	portName string,
) error {
	labels := infraLabels(name)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: flow.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Labels = labels
		svc.Spec.Selector = labels
		svc.Spec.Type = corev1.ServiceTypeClusterIP
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       portName,
			Port:       int32(port),
			TargetPort: intstr.FromInt32(int32(port)),
			Protocol:   corev1.ProtocolTCP,
		}}
		return controllerutil.SetControllerReference(flow, svc, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("could not reconcile Service %s: %w", name, err)
	}

	logf.FromContext(ctx).Info("Reconciled Service",
		"name", svc.Name, "result", result,
	)
	return nil
}

// infraLabels returns standard labels for control-plane infrastructure resources.
func infraLabels(name string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       name,
		"app.kubernetes.io/part-of":    "foundry-flow",
		"app.kubernetes.io/component":  "control-plane",
		"app.kubernetes.io/managed-by": "foundry-operator",
	}
}
