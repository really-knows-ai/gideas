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
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"k8s.io/utils/ptr"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

// DefaultCartographerImage is the compiled-in default for the Cartographer
// container image. Overridden at build time via -ldflags -X.
var DefaultCartographerImage = "flow-operator:latest"

// cartographerStorageSize is the default PVC storage size for Cartographer.
const cartographerStorageSize = "1Gi"

// labelsForCartographer returns the standard labels for Cartographer resources.
func (r *FoundryGraphReconciler) labelsForCartographer(fg *flowv1.FoundryGraph) map[string]string {
	return map[string]string{
		"app.kubernetes.io/component":  "cartographer",
		"app.kubernetes.io/name":       "cartographer",
		"app.kubernetes.io/instance":   fg.Name,
		"app.kubernetes.io/part-of":    "foundry-flow",
		"app.kubernetes.io/managed-by": managedByOperator,
	}
}

// cartographerServiceName returns the service name for the Cartographer.
func (r *FoundryGraphReconciler) cartographerServiceName(fg *flowv1.FoundryGraph) string {
	return "cartographer-" + fg.Name
}

// reconcilePVC creates or updates the PVC for the Cartographer's data directory.
func (r *FoundryGraphReconciler) reconcilePVC(ctx context.Context, fg *flowv1.FoundryGraph) error {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "data-" + fg.Name,
			Namespace: fg.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, pvc, func() error {
		var qty resource.Quantity
		if fg.Spec.Storage == nil || fg.Spec.Storage.Size == nil || fg.Spec.Storage.Size.IsZero() {
			qty = resource.MustParse(cartographerStorageSize)
		} else {
			qty = *fg.Spec.Storage.Size
		}
		// Clamp minimum to 1Mi.
		if qty.Value() < 1*1024*1024 {
			qty = resource.MustParse("1Mi")
		}
		// Only increase, never shrink.
		current := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
		if qty.Cmp(current) < 0 {
			qty = current
		}
		pvc.Labels = r.labelsForCartographer(fg)
		pvc.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
		pvc.Spec.Resources = corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceStorage: qty},
		}
		return controllerutil.SetControllerReference(fg, pvc, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("reconcile PVC: %w", err)
	}
	return nil
}

// reconcileRBAC creates or updates the ServiceAccount, Roles, and RoleBindings
// for the Cartographer.
func (r *FoundryGraphReconciler) reconcileRBAC(ctx context.Context, fg *flowv1.FoundryGraph) error {
	saName := "cartographer-" + fg.Name

	// ServiceAccount
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: fg.Namespace},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		sa.Labels = r.labelsForCartographer(fg)
		return controllerutil.SetControllerReference(fg, sa, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("reconcile ServiceAccount: %w", err)
	}

	// Role for key reader
	roleName := "cartographer-" + fg.Name + "-key-reader"
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: roleName, Namespace: fg.Namespace},
	}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, role, func() error {
		role.Labels = r.labelsForCartographer(fg)
		role.Rules = []rbacv1.PolicyRule{
			{
				APIGroups:     []string{""},
				Resources:     []string{"secrets"},
				ResourceNames: []string{"cartographer-operator-key", "cartographer-sidecar-key"},
				Verbs:         []string{"get"},
			},
		}
		return controllerutil.SetControllerReference(fg, role, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("reconcile key-reader Role: %w", err)
	}

	// RoleBinding for key reader
	rbName := "cartographer-" + fg.Name + "-key-reader"
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: rbName, Namespace: fg.Namespace},
	}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, rb, func() error {
		rb.Labels = r.labelsForCartographer(fg)
		rb.Subjects = []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: saName, Namespace: fg.Namespace},
		}
		rb.RoleRef = rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     roleName,
		}
		return controllerutil.SetControllerReference(fg, rb, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("reconcile key-reader RoleBinding: %w", err)
	}

	// Remote auth Role and RoleBinding if secretRef is set.
	if fg.Spec.Versioning != nil && fg.Spec.Versioning.Remote != nil && fg.Spec.Versioning.Remote.Auth != nil && fg.Spec.Versioning.Remote.Auth.SecretRef != "" {
		ref := fg.Spec.Versioning.Remote.Auth.SecretRef
		remoteRoleName := "cartographer-" + fg.Name + "-remote-auth"
		remoteRole := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: remoteRoleName, Namespace: fg.Namespace},
		}
		_, err = controllerutil.CreateOrUpdate(ctx, r.Client, remoteRole, func() error {
			remoteRole.Labels = r.labelsForCartographer(fg)
			remoteRole.Rules = []rbacv1.PolicyRule{
				{
					APIGroups:     []string{""},
					Resources:     []string{"secrets"},
					ResourceNames: []string{ref},
					Verbs:         []string{"get"},
				},
			}
			return controllerutil.SetControllerReference(fg, remoteRole, r.Scheme)
		})
		if err != nil {
			return fmt.Errorf("reconcile remote-auth Role: %w", err)
		}

		remoteRB := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: remoteRoleName, Namespace: fg.Namespace},
		}
		_, err = controllerutil.CreateOrUpdate(ctx, r.Client, remoteRB, func() error {
			remoteRB.Labels = r.labelsForCartographer(fg)
			remoteRB.Subjects = []rbacv1.Subject{
				{Kind: "ServiceAccount", Name: saName, Namespace: fg.Namespace},
			}
			remoteRB.RoleRef = rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     remoteRoleName,
			}
			return controllerutil.SetControllerReference(fg, remoteRB, r.Scheme)
		})
		if err != nil {
			return fmt.Errorf("reconcile remote-auth RoleBinding: %w", err)
		}
	} else {
		// Delete remote-auth Role and RoleBinding if they exist.
		remoteRoleName := "cartographer-" + fg.Name + "-remote-auth"
		remoteRole := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: remoteRoleName, Namespace: fg.Namespace}}
		if err := r.Get(ctx, client.ObjectKeyFromObject(remoteRole), remoteRole); err == nil {
			if delErr := r.Delete(ctx, remoteRole); delErr != nil {
				return fmt.Errorf("delete remote-auth Role: %w", delErr)
			}
		} else if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get remote-auth Role: %w", err)
		}

		remoteRB := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: remoteRoleName, Namespace: fg.Namespace}}
		if err := r.Get(ctx, client.ObjectKeyFromObject(remoteRB), remoteRB); err == nil {
			if delErr := r.Delete(ctx, remoteRB); delErr != nil {
				return fmt.Errorf("delete remote-auth RoleBinding: %w", delErr)
			}
		} else if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get remote-auth RoleBinding: %w", err)
		}
	}

	return nil
}

// deploymentEnvVars builds the environment variables for the Cartographer container.
func (r *FoundryGraphReconciler) deploymentEnvVars(fg *flowv1.FoundryGraph) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{
			Name:  "LADYBUG_DB_PATH",
			Value: "/data",
		},
		{
			Name:  "CARTOGRAPHER_PORT",
			Value: strconv.Itoa(int(r.CartographerPort)),
		},
		{
			Name: "TRANSACTION_TIMEOUT",
			Value: func() string {
				if fg.Spec.Versioning != nil && fg.Spec.Versioning.TransactionTimeout != nil && fg.Spec.Versioning.TransactionTimeout.Duration > 0 {
					return fg.Spec.Versioning.TransactionTimeout.Duration.String()
				}
				return "30m"
			}(),
		},
		{
			Name:  "REMOTE_PULL_ON_INIT",
			Value: strconv.FormatBool(fg.Spec.Versioning != nil && fg.Spec.Versioning.Remote != nil && fg.Spec.Versioning.Remote.PullOnInit),
		},
		// Secret-key refs
		{
			Name: "OPERATOR_VERIFICATION_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "cartographer-operator-key"},
					Key:                  "key",
				},
			},
		},
		{
			Name: "SIDECAR_VERIFICATION_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "cartographer-sidecar-key"},
					Key:                  "key",
				},
			},
		},
		// Downward API: pod namespace
		{
			Name: "POD_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.namespace",
				},
			},
		},
	}
	// Optional: remote URL
	if fg.Spec.Versioning != nil && fg.Spec.Versioning.Remote != nil && fg.Spec.Versioning.Remote.URL != "" {
		env = append(env, corev1.EnvVar{Name: "REMOTE_URL", Value: fg.Spec.Versioning.Remote.URL})
	}
	// Optional: remote auth secret ref
	if fg.Spec.Versioning != nil && fg.Spec.Versioning.Remote != nil && fg.Spec.Versioning.Remote.Auth != nil && fg.Spec.Versioning.Remote.Auth.SecretRef != "" {
		env = append(env, corev1.EnvVar{Name: "REMOTE_AUTH_SECRET_REF", Value: fg.Spec.Versioning.Remote.Auth.SecretRef})
	}
	// EVENT_BUS_ADDRESS
	if addr := r.EventBusAddress; addr != "" {
		env = append(env, corev1.EnvVar{Name: "EVENT_BUS_ADDRESS", Value: addr})
	}
	// CAPABILITY_STALENESS_WINDOW
	staleness := r.CapabilityStalenessWindow
	if staleness == "" {
		staleness = "30s"
	}
	env = append(env, corev1.EnvVar{Name: "CAPABILITY_STALENESS_WINDOW", Value: staleness})
	return env
}

// reconcileDeployment creates or updates the Cartographer Deployment.
func (r *FoundryGraphReconciler) reconcileDeployment(ctx context.Context, fg *flowv1.FoundryGraph) error {
	replicas := int32(1)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cartographer-" + fg.Name,
			Namespace: fg.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		labels := r.labelsForCartographer(fg)
		deploy.Labels = labels
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deploy.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec: corev1.PodSpec{
				// ponytail: PSa "restricted"-level SecurityContext
				SecurityContext: &corev1.PodSecurityContext{
					RunAsNonRoot:   ptr.To(true),
					SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
				},
				ServiceAccountName: "cartographer-" + fg.Name,
				Containers: []corev1.Container{{
					Name:            "cartographer",
					Image:           r.CartographerImage,
					ImagePullPolicy: corev1.PullIfNotPresent,
					// ponytail: restricted SecurityContext
					SecurityContext: &corev1.SecurityContext{
						ReadOnlyRootFilesystem:   ptr.To(true),
						AllowPrivilegeEscalation: ptr.To(false),
						Capabilities: &corev1.Capabilities{
							Drop: []corev1.Capability{"ALL"},
						},
					},
					Ports: []corev1.ContainerPort{
						{Name: "grpc", ContainerPort: r.CartographerPort, Protocol: corev1.ProtocolTCP},
					},
					Env: r.deploymentEnvVars(fg),
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							GRPC: &corev1.GRPCAction{
								Port:    r.CartographerPort,
								Service: nil,
							},
						},
						InitialDelaySeconds: 5,
						PeriodSeconds:       10,
					},
					// ponytail: Provisional resource requests/limits
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("10m"),
							corev1.ResourceMemory: resource.MustParse("64Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "data", MountPath: "/data"},
					},
				}},
				Volumes: []corev1.Volume{
					{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "data-" + fg.Name,
							},
						},
					},
				},
			},
		}
		return controllerutil.SetControllerReference(fg, deploy, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("reconcile Deployment: %w", err)
	}
	return nil
}

// reconcileService creates or updates the Cartographer ClusterIP Service.
func (r *FoundryGraphReconciler) reconcileService(ctx context.Context, fg *flowv1.FoundryGraph) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cartographer-" + fg.Name,
			Namespace: fg.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		labels := r.labelsForCartographer(fg)
		svc.Labels = labels
		svc.Spec.Selector = labels
		svc.Spec.Type = corev1.ServiceTypeClusterIP
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       "grpc",
			Port:       r.CartographerPort,
			TargetPort: intstr.FromInt32(r.CartographerPort),
			Protocol:   corev1.ProtocolTCP,
		}}
		return controllerutil.SetControllerReference(fg, svc, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("reconcile Service: %w", err)
	}
	return nil
}
