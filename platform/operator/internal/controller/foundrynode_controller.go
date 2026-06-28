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
	"regexp"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	flowv1 "github.com/gideas/flow/operator/api/v1"
)

const (
	// sidecarImage is the container image for the Sidecar injected into every node pod.
	sidecarImage = "flow-sidecar:dbg2"

	// sidecarGRPCPort is the port the Sidecar listens on for gRPC.
	sidecarGRPCPort = 50051

	// nodeContainerName is the name of the node container in the pod template.
	nodeContainerName = "node"

	// sidecarContainerName is the name of the Sidecar container in the pod template.
	sidecarContainerName = "sidecar"

	// phaseInitialising is the initial phase for resources being set up.
	phaseInitialising = "Initialising"

	// phaseReady indicates the resource is fully reconciled and operational.
	phaseReady = "Ready"

	// phaseDegraded indicates the resource has issues but may be partially functional.
	phaseDegraded = "Degraded"

	// phaseFailed indicates the resource has failed validation or reconciliation.
	phaseFailed = "Failed"

	// phaseStopped indicates the resource is intentionally stopped (scale-to-zero).
	phaseStopped = "Stopped"

	// strategyStatefulSet is the StatefulSet deployment strategy.
	strategyStatefulSet = "StatefulSet"

	// defaultStorageSize is the default PVC size when not specified.
	defaultStorageSize = "1Gi"

	// conditionReady is the standard Ready condition type.
	conditionReady = "Ready"
)

// capabilityPattern validates VERB:RESOURCE[/QUALIFIER] capability strings.
var capabilityPattern = regexp.MustCompile(
	`^(READ|WRITE|STAMP|USE|CREATE):` +
		`(artefact|law|friction|flow|workitem|feedback|support|queue)` +
		`(/[a-zA-Z0-9_*-]+(/[a-zA-Z0-9_*-]+)?)?$`,
)

// FoundryNodeReconciler reconciles a FoundryNode object.
//
// Responsibilities:
//   - Validate capabilities, contract bindings, timeout against Flow-level constraints.
//   - Determine deployment strategy (ReplicaSet vs StatefulSet).
//   - Create/update the Deployment or StatefulSet for the node, injecting the Sidecar.
//   - Create a Headless Service when USE:queue/server capability is present.
//   - Update status conditions to reflect reconciliation health.
type FoundryNodeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=flow.gideas.io,resources=foundrynodes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=flow.gideas.io,resources=foundrynodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=flow.gideas.io,resources=foundrynodes/finalizers,verbs=update
// +kubebuilder:rbac:groups=flow.gideas.io,resources=foundryflows,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

// Reconcile validates a FoundryNode against Flow-level constraints and ensures
// the corresponding Deployment/StatefulSet exists with a Sidecar injected.
func (r *FoundryNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the FoundryNode instance.
	var node flowv1.FoundryNode
	if err := r.Get(ctx, req.NamespacedName, &node); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Reconciling FoundryNode",
		"name", node.Name,
		"namespace", node.Namespace,
	)

	// Validate capability syntax.
	if err := r.validateCapabilities(&node); err != nil {
		return SetStatusCondition(ctx, r.Client, &node, conditionReady, metav1.ConditionFalse, "InvalidCapability", err.Error(),
			func(n *flowv1.FoundryNode) *[]metav1.Condition { return &n.Status.Conditions },
		)
	}

	// Validate against Flow-level constraints.
	if err := r.validateAgainstFlow(ctx, &node); err != nil {
		return SetStatusCondition(ctx, r.Client, &node, conditionReady, metav1.ConditionFalse, "ValidationFailed", err.Error(),
			func(n *flowv1.FoundryNode) *[]metav1.Condition { return &n.Status.Conditions },
		)
	}

	// Validate routing output targets exist.
	if err := r.validateOutputTargets(ctx, &node); err != nil {
		return SetStatusCondition(ctx, r.Client, &node, conditionReady, metav1.ConditionFalse, "InvalidOutputTarget", err.Error(),
			func(n *flowv1.FoundryNode) *[]metav1.Condition { return &n.Status.Conditions },
		)
	}

	// Validate child Workitem contract stamp references.
	if err := r.validateChildContracts(ctx, &node); err != nil {
		return SetStatusCondition(ctx, r.Client, &node, conditionReady, metav1.ConditionFalse, "InvalidChildContract", err.Error(),
			func(n *flowv1.FoundryNode) *[]metav1.Condition { return &n.Status.Conditions },
		)
	}

	// Check if the parent FoundryFlow has federation configured.
	fedEnabled := r.flowHasFederation(ctx, &node)

	// Determine deployment strategy.
	useStatefulSet := r.requiresStatefulSet(&node)

	// Reconcile the workload (Deployment or StatefulSet).
	if useStatefulSet {
		if err := r.reconcileStatefulSet(ctx, &node, fedEnabled); err != nil {
			return SetStatusCondition(ctx, r.Client, &node, conditionReady, metav1.ConditionFalse, "ReconcileFailed", err.Error(),
				func(n *flowv1.FoundryNode) *[]metav1.Condition { return &n.Status.Conditions },
			)
		}
	} else {
		if err := r.reconcileDeployment(ctx, &node, fedEnabled); err != nil {
			return SetStatusCondition(ctx, r.Client, &node, conditionReady, metav1.ConditionFalse, "ReconcileFailed", err.Error(),
				func(n *flowv1.FoundryNode) *[]metav1.Condition { return &n.Status.Conditions },
			)
		}
	}

	// Reconcile Headless Service for USE:queue/server nodes.
	if r.hasQueueServerCapability(&node) {
		if err := r.reconcileHeadlessService(ctx, &node); err != nil {
			return SetStatusCondition(ctx, r.Client, &node, conditionReady, metav1.ConditionFalse, "ServiceReconcileFailed", err.Error(),
				func(n *flowv1.FoundryNode) *[]metav1.Condition { return &n.Status.Conditions },
			)
		}
	}

	return SetStatusCondition(ctx, r.Client, &node, conditionReady, metav1.ConditionTrue, "Reconciled", "Node workload reconciled successfully",
		func(n *flowv1.FoundryNode) *[]metav1.Condition { return &n.Status.Conditions },
	)
}

// validateCapabilities checks that all capability strings match the grammar.
func (r *FoundryNodeReconciler) validateCapabilities(node *flowv1.FoundryNode) error {
	for _, cap := range node.Spec.Capabilities {
		if !capabilityPattern.MatchString(cap) {
			return fmt.Errorf("invalid capability syntax: %q", cap)
		}
	}
	return nil
}

// validateAgainstFlow validates the node against its parent FoundryFlow constraints.
func (r *FoundryNodeReconciler) validateAgainstFlow(ctx context.Context, node *flowv1.FoundryNode) error {
	// List FoundryFlows in the same namespace. Expect exactly one.
	var flows flowv1.FoundryFlowList
	if err := r.List(ctx, &flows, client.InNamespace(node.Namespace)); err != nil {
		return fmt.Errorf("could not list FoundryFlows: %w", err)
	}

	if len(flows.Items) == 0 {
		// No FoundryFlow yet — skip flow-level validation.
		return nil
	}

	flow := &flows.Items[0]

	// Validate entry contract binding.
	if node.Spec.Entry != "" {
		if _, ok := flow.Spec.EntryContracts[node.Spec.Entry]; !ok {
			return fmt.Errorf("entry binding %q does not reference a defined entry contract on FoundryFlow %q", node.Spec.Entry, flow.Name)
		}
	}

	// Validate exit contract binding.
	if node.Spec.Exit != "" {
		if _, ok := flow.Spec.ExitContracts[node.Spec.Exit]; !ok {
			return fmt.Errorf("exit binding %q does not reference a defined exit contract on FoundryFlow %q", node.Spec.Exit, flow.Name)
		}
	}

	// Validate timeout does not exceed maxTimeout.
	if node.Spec.Timeout != nil {
		maxTimeout := flow.Spec.GovernancePolicy.MaxTimeout.Duration
		if node.Spec.Timeout.Duration > maxTimeout {
			return fmt.Errorf("node timeout %v exceeds Flow maxTimeout %v", node.Spec.Timeout.Duration, maxTimeout)
		}
	}

	return nil
}

// flowHasFederation checks if the parent FoundryFlow has federation configured.
func (r *FoundryNodeReconciler) flowHasFederation(ctx context.Context, node *flowv1.FoundryNode) bool {
	var flows flowv1.FoundryFlowList
	if err := r.List(ctx, &flows, client.InNamespace(node.Namespace)); err != nil {
		return false
	}
	if len(flows.Items) == 0 {
		return false
	}
	flow := &flows.Items[0]
	return flow.Spec.CrossFlow != nil && flow.Spec.CrossFlow.Federation != nil
}

// validateOutputTargets checks that routing output targets reference existing FoundryNodes.
func (r *FoundryNodeReconciler) validateOutputTargets(ctx context.Context, node *flowv1.FoundryNode) error {
	for _, output := range node.Spec.Outputs {
		var target flowv1.FoundryNode
		if err := r.Get(ctx, types.NamespacedName{
			Name:      output.Target,
			Namespace: node.Namespace,
		}, &target); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("output %q targets nonexistent node %q", output.Name, output.Target)
			}
			return fmt.Errorf("could not verify output target %q: %w", output.Target, err)
		}
	}
	return nil
}

// validateChildContracts checks that child Workitem contract stamp references
// resolve against GovernedArtefact vocabularies.
func (r *FoundryNodeReconciler) validateChildContracts(ctx context.Context, node *flowv1.FoundryNode) error {
	if node.Spec.ChildWorkitems == nil {
		return nil
	}

	// Build GovernedArtefact vocabularies.
	var artefacts flowv1.GovernedArtefactList
	if err := r.List(ctx, &artefacts, client.InNamespace(node.Namespace)); err != nil {
		return fmt.Errorf("could not list GovernedArtefacts: %w", err)
	}
	vocabularies := make(map[string]map[string]bool)
	for _, ga := range artefacts.Items {
		stamps := make(map[string]bool)
		for _, s := range ga.Spec.Stamps {
			stamps[s] = true
		}
		vocabularies[ga.Name] = stamps
	}

	// Validate entry contract stamps.
	for gaName, requiredStamps := range node.Spec.ChildWorkitems.EntryContract {
		vocab, exists := vocabularies[gaName]
		if !exists {
			continue // GovernedArtefact not yet created.
		}
		for _, stamp := range requiredStamps {
			if !vocabMatch(vocab, stamp) {
				return fmt.Errorf("child entry contract references stamp %q not in GovernedArtefact %q vocabulary",
					stamp, gaName)
			}
		}
	}

	// Validate exit contract stamps.
	for gaName, requiredStamps := range node.Spec.ChildWorkitems.ExitContract {
		vocab, exists := vocabularies[gaName]
		if !exists {
			continue // GovernedArtefact not yet created.
		}
		for _, stamp := range requiredStamps {
			if !vocabMatch(vocab, stamp) {
				return fmt.Errorf("child exit contract references stamp %q not in GovernedArtefact %q vocabulary",
					stamp, gaName)
			}
		}
	}

	return nil
}

// requiresStatefulSet determines if the node needs StatefulSet deployment.
func (r *FoundryNodeReconciler) requiresStatefulSet(node *flowv1.FoundryNode) bool {
	// Explicit storage config with StatefulSet strategy.
	if node.Spec.Storage != nil && node.Spec.Storage.DeploymentStrategy == strategyStatefulSet {
		return true
	}
	// USE:queue/server capability requires StatefulSet.
	return r.hasQueueServerCapability(node)
}

// hasQueueServerCapability checks if the node has USE:queue/server.
func (r *FoundryNodeReconciler) hasQueueServerCapability(node *flowv1.FoundryNode) bool {
	return slices.Contains(node.Spec.Capabilities, "USE:queue/server")
}

// buildPodTemplate constructs the pod template spec with node + sidecar containers.
func (r *FoundryNodeReconciler) buildPodTemplate(node *flowv1.FoundryNode, hasFederation bool) corev1.PodTemplateSpec {
	labels := r.labelsForNode(node)

	// Node container.
	nodeContainer := corev1.Container{
		Name:            nodeContainerName,
		Image:           node.Spec.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Env: []corev1.EnvVar{
			{Name: "FLOW_NODE_NAME", Value: node.Name},
			{Name: "FLOW_NAMESPACE", Value: node.Namespace},
			{Name: "SIDECAR_ADDRESS", Value: fmt.Sprintf("localhost:%d", sidecarGRPCPort)},
			// Direct access: Event Bus uses gRPC streaming which bypasses the Sidecar proxy.
			{Name: "EVENT_BUS_ADDRESS", Value: fmt.Sprintf("%s:%d", eventBusServiceName, eventBusPort)},
			{Name: "OLLAMA_BASE_URL", Value: "http://host.docker.internal:11434"},
		},
	}

	// Sidecar container.
	sidecarContainer := corev1.Container{
		Name:            sidecarContainerName,
		Image:           sidecarImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Ports: []corev1.ContainerPort{
			{Name: "grpc", ContainerPort: int32(sidecarGRPCPort), Protocol: corev1.ProtocolTCP},
		},
		Env: []corev1.EnvVar{
			{Name: "FLOW_NODE_NAME", Value: node.Name},
			{Name: "FLOW_NAMESPACE", Value: node.Namespace},
			// Wire control-plane infrastructure service addresses.
			{Name: "ARCHIVIST_ADDRESS", Value: fmt.Sprintf("%s:%d", archivistSvcName, archivistPort)},
			{Name: "EVENT_BUS_ADDRESS", Value: fmt.Sprintf("%s:%d", eventBusServiceName, eventBusPort)},
			{Name: "FRICTION_LEDGER_ADDRESS", Value: fmt.Sprintf("%s:%d", frictionLedgerSvcNm, frictionLedgerPort)},
			{Name: "OPERATOR_ADDRESS", Value: fmt.Sprintf("%s:%d", operatorSvcName, operatorPort)},
			{Name: "LIBRARIAN_ADDRESS", Value: fmt.Sprintf("%s:%d", librarianSvcName, librarianPort)},
		},
	}

	// Inject capabilities as comma-separated env var for the Sidecar.
	if len(node.Spec.Capabilities) > 0 {
		sidecarContainer.Env = append(sidecarContainer.Env, corev1.EnvVar{
			Name:  "FLOW_CAPABILITIES",
			Value: strings.Join(node.Spec.Capabilities, ","),
		})
	}

	// Inject federation address when the parent FoundryFlow has federation configured.
	if hasFederation {
		fedAddr := corev1.EnvVar{
			Name:  "FEDERATION_ADDRESS",
			Value: fmt.Sprintf("%s:%d", federationSvcName, federationPort),
		}
		sidecarContainer.Env = append(sidecarContainer.Env, fedAddr)
	}

	// Inject volume mounts if storage is configured.
	if node.Spec.Storage != nil {
		for _, vol := range node.Spec.Storage.Volumes {
			mount := corev1.VolumeMount{
				Name:      vol.Name,
				MountPath: vol.MountPath,
			}
			nodeContainer.VolumeMounts = append(nodeContainer.VolumeMounts, mount)
			sidecarContainer.VolumeMounts = append(sidecarContainer.VolumeMounts, mount)
		}
	}

	// Mount node config from a ConfigMap named <nodeName>-config if it exists.
	configName := node.Name + "-config"
	nodeContainer.VolumeMounts = append(nodeContainer.VolumeMounts, corev1.VolumeMount{
		Name:      "node-config",
		ReadOnly:  true,
		MountPath: "/etc/foundry",
	})

	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: labels,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{nodeContainer, sidecarContainer},
			Volumes: []corev1.Volume{
				{
					Name: "node-config",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: configName,
							},
							DefaultMode: func() *int32 { v := int32(420); return &v }(),
						},
					},
				},
			},
		},
	}
}

// reconcileDeployment creates or updates a Deployment for the FoundryNode.
func (r *FoundryNodeReconciler) reconcileDeployment(ctx context.Context, node *flowv1.FoundryNode, hasFederation bool) error {
	replicas := int32(1)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      node.Name,
			Namespace: node.Namespace,
		},
	}

	// Preserve the pod-template-hash label set by the Deployment controller
	// to avoid an infinite reconcile loop.
	prevHash := ""
	if deploy.Spec.Template.Labels != nil {
		prevHash = deploy.Spec.Template.Labels["pod-template-hash"]
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		deploy.Labels = r.labelsForNode(node)
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{
			MatchLabels: r.labelsForNode(node),
		}
		deploy.Spec.Template = r.buildPodTemplate(node, hasFederation)
		if prevHash != "" {
			if deploy.Spec.Template.Labels == nil {
				deploy.Spec.Template.Labels = make(map[string]string)
			}
			deploy.Spec.Template.Labels["pod-template-hash"] = prevHash
		}
		return controllerutil.SetControllerReference(node, deploy, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("could not reconcile Deployment: %w", err)
	}

	logf.FromContext(ctx).Info("Reconciled Deployment",
		"name", deploy.Name,
		"result", result,
	)
	return nil
}

// reconcileStatefulSet creates or updates a StatefulSet for the FoundryNode.
func (r *FoundryNodeReconciler) reconcileStatefulSet(ctx context.Context, node *flowv1.FoundryNode, hasFederation bool) error {
	replicas := int32(1)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      node.Name,
			Namespace: node.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, sts, func() error {
		sts.Labels = r.labelsForNode(node)
		sts.Spec.Replicas = &replicas
		sts.Spec.ServiceName = node.Name
		sts.Spec.Selector = &metav1.LabelSelector{
			MatchLabels: r.labelsForNode(node),
		}
		sts.Spec.Template = r.buildPodTemplate(node, hasFederation)

		// Build VolumeClaimTemplates from storage config.
		if node.Spec.Storage != nil {
			var pvcs []corev1.PersistentVolumeClaim
			for _, vol := range node.Spec.Storage.Volumes {
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

		return controllerutil.SetControllerReference(node, sts, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("could not reconcile StatefulSet: %w", err)
	}

	logf.FromContext(ctx).Info("Reconciled StatefulSet",
		"name", sts.Name,
		"result", result,
	)
	return nil
}

// reconcileHeadlessService creates or updates a Headless Service for queue/server nodes.
func (r *FoundryNodeReconciler) reconcileHeadlessService(ctx context.Context, node *flowv1.FoundryNode) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      node.Name,
			Namespace: node.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Labels = r.labelsForNode(node)
		svc.Spec.ClusterIP = corev1.ClusterIPNone
		svc.Spec.Selector = r.labelsForNode(node)
		svc.Spec.Ports = []corev1.ServicePort{
			{
				Name:     "grpc",
				Port:     int32(sidecarGRPCPort),
				Protocol: corev1.ProtocolTCP,
			},
		}
		return controllerutil.SetControllerReference(node, svc, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("could not reconcile Headless Service: %w", err)
	}

	logf.FromContext(ctx).Info("Reconciled Headless Service",
		"name", svc.Name,
		"result", result,
	)
	return nil
}

// labelsForNode returns standard labels for resources owned by this FoundryNode.
func (r *FoundryNodeReconciler) labelsForNode(node *flowv1.FoundryNode) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "foundrynode",
		"app.kubernetes.io/instance":   node.Name,
		"app.kubernetes.io/managed-by": managedByOperator,
		"flow.gideas.io/node":          node.Name,
		"flow.gideas.io/node-name":     node.Name,
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *FoundryNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&flowv1.FoundryNode{}).
		Owns(&appsv1.Deployment{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Named("foundrynode").
		Complete(r)
}
