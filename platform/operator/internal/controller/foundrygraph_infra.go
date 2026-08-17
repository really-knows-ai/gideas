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

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

// DefaultCartographerImage is the compiled-in default for the Cartographer
// container image. Overridden at build time via -ldflags -X.
var DefaultCartographerImage = "flow-operator:latest"

// cartographerStorageSize is the default PVC storage size for Cartographer.
const cartographerStorageSize = "1Gi"

// DefaultTransactionTimeout is the SPEC R5 TRANSACTION_TIMEOUT fallback rendered into the
// Cartographer Deployment env (foundrygraph_infra.go) when versioning.transactionTimeout is
// unset. It is the single operator-side source of truth for this default; the operator
// module references it here rather than repeating the "30m" literal.
const DefaultTransactionTimeout = "30m"

// DefaultCapabilityStalenessWindow is the SPEC R5 CAPABILITY_STALENESS_WINDOW fallback
// rendered into the Cartographer Deployment env (foundrygraph_infra.go) when the
// operator's CapabilityStalenessWindow is empty, and used by cmd/main.go's
// resolveCapabilityStalenessWindow. It is the single operator-side source of truth for this
// default; the operator module references it here rather than repeating the "30s" literal.
const DefaultCapabilityStalenessWindow = "30s"

// cartographerTerminationGraceSecs is the Deployment's termination grace period. It must
// exceed the in-process GracefulStop drain (~30s) so durability teardown completes before
// kubelet SIGKILL; 100s matches the cartographer deployment.yaml reference template.
const cartographerTerminationGraceSecs = int64(100)

// cartographerStorageSizeAnnotation marks the effective desired PVC data size on the
// Cartographer Deployment pod template. Encoding the size into the pod template makes a
// storage.size-only spec change produce a pod-template delta (forcing a Deployment
// rollout) rather than silently patching only the PVC — SPEC R6 requires a storage change
// to redeploy the pod so the readiness → re-apply-schema sequence runs on the new pod.
const cartographerStorageSizeAnnotation = "flow.foundry.io/cartographer-storage-size"

// desiredStorageSize returns the effective desired PVC data size for a FoundryGraph:
// defaulted to cartographerStorageSize when unset and clamped to a 1Mi minimum (SPEC R6
// step 1). It does not apply reconcilePVC's never-shrink retention (that needs the live
// PVC), so callers combine it against the current PVC when they must preserve a larger size.
func desiredStorageSize(fg *flowv1.FoundryGraph) resource.Quantity {
	var qty resource.Quantity
	if fg.Spec.Storage == nil || fg.Spec.Storage.Size == nil || fg.Spec.Storage.Size.IsZero() {
		qty = resource.MustParse(cartographerStorageSize)
	} else {
		qty = *fg.Spec.Storage.Size
	}
	if qty.Cmp(resource.MustParse("1Mi")) < 0 {
		qty = resource.MustParse("1Mi")
	}
	return qty
}

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

// reconcileInfrastructure runs the SPEC R6 steps 4-8 infra reconciles in order:
// PVC, Secrets, RBAC, Deployment, Service. Extracted from Reconcile so the
// reconcile loop stays under the gocyclo complexity limit; each step is
// idempotent and any failure is returned to the caller's failure path.
func (r *FoundryGraphReconciler) reconcileInfrastructure(ctx context.Context, fg *flowv1.FoundryGraph) error {
	if err := r.reconcilePVC(ctx, fg); err != nil {
		return err
	}
	if err := r.reconcileSecrets(ctx, fg); err != nil {
		return err
	}
	if err := r.reconcileRBAC(ctx, fg); err != nil {
		return err
	}
	if err := r.reconcileDeployment(ctx, fg); err != nil {
		return err
	}
	if err := r.reconcileService(ctx, fg); err != nil {
		return err
	}
	return nil
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
		qty := desiredStorageSize(fg)
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
			// ponytail: the operator-side copy of this default is hoisted to the shared
			// DefaultTransactionTimeout constant, but the same "30m" default lives on in
			// four other surfaces that must stay in sync with it: (1) the cartographer
			// main.go getEnv("TRANSACTION_TIMEOUT", "30m") fallback
			// (platform/cartographer/cmd/main.go), (2) the reference template
			// platform/cartographer/deployment.yaml (TRANSACTION_TIMEOUT: "30m"), (3) the
			// SPEC R5 default `versioning.transactionTimeout: 30m`
			// (plans/cartographer/SPEC.md), and (4) the CRD-level kubebuilder default on
			// VersioningSpec.TransactionTimeout (foundrygraph_types.go, also "30m").
			// Changing the default in any one of those surfaces silently diverges the
			// others: a deployment that omits this env falls back to main.go's hardcoded
			// default, and a spec that omits transactionTimeout falls back here — if only
			// one copy is edited, the rendered Deployment enforces a different timeout
			// than documented. Ceiling: the cross-module/config copies have no single
			// compile-time-backed source of truth. Upgrade path: hoist those remaining
			// copies (cartographer main.go, deployment.yaml, CRD default) to one shared
			// constant read by all surfaces.
			Value: func() string {
				if fg.Spec.Versioning != nil && fg.Spec.Versioning.TransactionTimeout != nil && fg.Spec.Versioning.TransactionTimeout.Duration > 0 {
					return fg.Spec.Versioning.TransactionTimeout.Duration.String()
				}
				return DefaultTransactionTimeout
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
	// ponytail: the operator-side copies of this default (this Deployment env fallback and
	// cmd/main.go's resolveCapabilityStalenessWindow) are hoisted to the shared
	// DefaultCapabilityStalenessWindow constant, but the same "30s" default lives on in
	// three other surfaces that must stay in sync: (1) the cartographer main.go
	// getEnv("CAPABILITY_STALENESS_WINDOW", "30s") fallback
	// (platform/cartographer/cmd/main.go), (2) the reference template
	// platform/cartographer/deployment.yaml (commented-out CAPABILITY_STALENESS_WINDOW:
	// "30s"), and (3) the SPEC R5 environment-variable table default `30s`
	// (plans/cartographer/SPEC.md). Changing the default in any one place silently diverges
	// the others: a Cartographer pod that omits this env falls back to its own main.go
	// default, and the Operator's proxy anti-replay window (cmd/main.go) is validated
	// independently — if only one copy is edited, the staleness window enforced at the
	// Cartographer diverges from the operator's and from the SPEC's. Ceiling: the
	// cross-module/config copies have no single compile-time-backed source of truth. Upgrade
	// path: hoist those remaining copies (cartographer main.go, deployment.yaml, SPEC) to
	// one shared constant read by all surfaces.
	staleness := r.CapabilityStalenessWindow
	if staleness == "" {
		staleness = DefaultCapabilityStalenessWindow
	}
	env = append(env, corev1.EnvVar{Name: "CAPABILITY_STALENESS_WINDOW", Value: staleness})
	return env
}

// reconcileDeployment creates or updates the Cartographer Deployment.
func (r *FoundryGraphReconciler) reconcileDeployment(ctx context.Context, fg *flowv1.FoundryGraph) error {
	replicas := int32(1)
	termGrace := cartographerTerminationGraceSecs
	// resource.Quantity is a value type; assign to an addressable local before calling the
	// pointer-receiver String() method.
	storageSizeQty := desiredStorageSize(fg)
	storageSizeString := storageSizeQty.String()
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
			ObjectMeta: metav1.ObjectMeta{
				Labels: labels,
				// The desired storage size is part of the pod template so a storage.size
				// change (SPEC R6: non-schema fields like storage.size trigger redeployment)
				// alters the template hash and rolls a new pod, driving the readiness →
				// re-apply-schema sequence the SPEC requires, rather than only patching the
				// PVC in place.
				Annotations: map[string]string{
					cartographerStorageSizeAnnotation: storageSizeString,
				},
			},
			Spec: corev1.PodSpec{
				// ponytail: PSa "restricted"-level SecurityContext — the image must
				// run as non-root UID 65532:65532 (Dockerfile USER 65532:65532) under
				// seccomp (RuntimeDefault), and this pod-level profile enforces that
				// contract at the cluster boundary. Consequences of weakening it:
				// RunAsUser/RunAsGroup/FSGroup 65532 make a root-owned PVC
				// (hostpath / EBS static PVs) group-owned by 65532 and thus writable —
				// dropping or changing them makes /data unwritable from UID 65532 and
				// main.lbug/graph-repo/ creation fails at startup (ladybug.Open at
				// cmd/main.go:113), and dropping SeccompProfile removes the
				// kernel-syscall isolation RuntimeDefault provides. Failure mode: if
				// the cluster cannot satisfy the profile — an admission-time Pod
				// Security admission / Gatekeeper policy that rejects RuntimeDefault
				// seccomp or the non-root UID choice — the pod is refused at admission
				// or fails at container start and the FoundryGraph never becomes ready
				// until the cluster policy is aligned. Ceiling: the profile is
				// hardcoded here rather than derived from cluster policy. Upgrade
				// path: harden toward the PSA "restricted" profile only (never weaken
				// to "baseline" or drop seccomp); extend the profile only if a
				// cluster enforces an even stricter policy this one must match.
				SecurityContext: &corev1.PodSecurityContext{
					RunAsNonRoot:        new(true),
					RunAsUser:           new(int64(65532)),
					RunAsGroup:          new(int64(65532)),
					FSGroup:             new(int64(65532)),
					FSGroupChangePolicy: fsGroupChangePolicyPtr(corev1.FSGroupChangeOnRootMismatch),
					SeccompProfile:      &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
				},
				// terminationGracePeriodSeconds must exceed the in-process GracefulStop drain
				// budget so the durability teardown (StopGC, LADYBUGDB close, git restore)
				// finishes before kubelet SIGKILL. 100s matches the cartographer
				// deployment.yaml convention (SPEC R6: the Operator creates the Deployment
				// dynamically from that reference template).
				TerminationGracePeriodSeconds: &termGrace,
				ServiceAccountName:            "cartographer-" + fg.Name,
				Containers: []corev1.Container{{
					Name:            "cartographer",
					Image:           r.CartographerImage,
					ImagePullPolicy: corev1.PullIfNotPresent,
					// ponytail: restricted SecurityContext — ReadOnlyRootFilesystem=true
					// makes the container rootfs unwritable (only the /data PVC mount and
					// the /tmp emptyDir are writable), with privilege escalation denied
					// and ALL capabilities dropped. The /tmp emptyDir is mounted because
					// main.go's SSH known_hosts handling calls
					// os.CreateTemp("", "known_hosts-*") (cmd/main.go:800), which
					// resolves to os.TempDir() → /tmp; without a writable /tmp that
					// temp-file creation fails EROFS and a remote auth secret carrying a
					// known_hosts key breaks (auth construction errors out and the remote
					// pull / pull-on-init fails). Failure mode: silent at render time — a
					// rootfs-writing image change compiles and deploys but breaks only at
					// runtime (EROFS on a write outside /data and /tmp: startup or
					// remote-sync failure), never surfacing in the rendered Deployment.
					// Ceiling: writable locations are explicit (only /data and /tmp), so
					// the constraint is invisible until a write hits it. Upgrade path: if
					// the image needs additional scratch space, mount an explicit emptyDir
					// for it or write to /data and keep the rootfs read-only.
					SecurityContext: &corev1.SecurityContext{
						ReadOnlyRootFilesystem:   new(true),
						AllowPrivilegeEscalation: new(false),
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
					// ponytail: Provisional resource requests/limits — 10m CPU request,
					// 64Mi memory request, 500m CPU limit, 128Mi memory limit are
					// placeholders chosen before production measurement. Consequences:
					// at the 128Mi memory limit the kernel OOM-killer kills the
					// container once in-memory LadybugDB data, transaction state, or
					// sync buffering exceeds 128Mi (CrashLoopBackOff); at the 10m CPU
					// request the scheduler can co-locate the pod on a contended node
					// where it is throttled to ~1% of a core, stalling startup (schema
					// apply, re-hydration) and the sync cycle. Failure mode: both
					// limits fail silently — an OOM-killed container restarts without a
					// reconcile error and CPU throttling degrades latency without any
					// event, so the readiness probe (unavailable pod) is the only
					// visible symptom. Ceiling: no measured baseline. Upgrade path:
					// measure steady-state and peak usage (metrics-server / kubectl top)
					// under representative load, then set requests to observed p50/p95
					// usage and limits to bounded headroom above peak, re-verifying the
					// readiness probe stays green.
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
						// SPEC R1 ssh:// known_hosts handling writes via
						// os.CreateTemp("", "known_hosts-*") (cmd/main.go:800), which
						// resolves to os.TempDir() → /tmp; under ReadOnlyRootFilesystem
						// the rootfs /tmp is read-only, so this emptyDir keeps the
						// known_hosts temp write working (EROFS otherwise).
						{Name: "tmp", MountPath: "/tmp"},
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
					// Scratch space for the read-only rootfs: os.CreateTemp default-dir
					// writes (known_hosts, cmd/main.go:800) land on /tmp. emptyDir is
					// per-pod scratch; nothing durable is stored here.
					{
						Name: "tmp",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
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

// fsGroupChangePolicyPtr returns a pointer to a PodFSGroupChangePolicy value.
//
// The body relies on Go 1.26 new(expr) semantics (go.mod requires go 1.26.0):
// new(p) allocates a fresh variable holding the resolved value of the expression
// p and returns its address. This is NOT the classic new(T) zero-allocation,
// whose argument is a type and would be discarded — do not "simplify" it to
// new(corev1.PodFSGroupChangePolicy) (which would return a pointer to a zero
// value, dropping p) or to returning &p (which would alias the function
// parameter).
//
//go:fix inline
func fsGroupChangePolicyPtr(p corev1.PodFSGroupChangePolicy) *corev1.PodFSGroupChangePolicy {
	return new(p)
}
