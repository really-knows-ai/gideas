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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	flowv1 "github.com/gideas/flow/operator/api/v1"
)

var _ = Describe("FoundryFlow Controller", func() {
	Context("When reconciling a valid resource", func() {
		const resourceName = "test-flow"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			By("creating the custom resource for the Kind FoundryFlow")
			var existing flowv1.FoundryFlow
			err := k8sClient.Get(ctx, typeNamespacedName, &existing)
			if err != nil && errors.IsNotFound(err) {
				resource := &flowv1.FoundryFlow{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: flowv1.FoundryFlowSpec{
						EntryContracts: map[string]flowv1.Contract{
							"default": {},
						},
						ExitContracts: map[string]flowv1.Contract{
							"default": {},
						},
						GovernancePolicy: flowv1.GovernancePolicy{
							MaxVisits:      10,
							DefaultTimeout: metav1.Duration{Duration: 5 * time.Minute},
							MaxTimeout:     metav1.Duration{Duration: 30 * time.Minute},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &flowv1.FoundryFlow{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance FoundryFlow")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should reconcile to Ready phase with valid config", func() {
			By("Reconciling the created resource")
			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the status is Ready")
			var flow flowv1.FoundryFlow
			Expect(k8sClient.Get(ctx, typeNamespacedName, &flow)).To(Succeed())
			Expect(flow.Status.Phase).To(Equal("Ready"))

			readyCond := meta.FindStatusCondition(flow.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(readyCond.Reason).To(Equal("Reconciled"))
		})
	})

	Context("When governance policy is invalid", func() {
		const resourceName = "test-flow-invalid"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			By("creating a FoundryFlow with maxTimeout < defaultTimeout")
			var existing flowv1.FoundryFlow
			err := k8sClient.Get(ctx, typeNamespacedName, &existing)
			if err != nil && errors.IsNotFound(err) {
				resource := &flowv1.FoundryFlow{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: flowv1.FoundryFlowSpec{
						EntryContracts: map[string]flowv1.Contract{
							"default": {},
						},
						ExitContracts: map[string]flowv1.Contract{
							"default": {},
						},
						GovernancePolicy: flowv1.GovernancePolicy{
							MaxVisits:      10,
							DefaultTimeout: metav1.Duration{Duration: 30 * time.Minute},
							MaxTimeout:     metav1.Duration{Duration: 5 * time.Minute},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &flowv1.FoundryFlow{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance FoundryFlow")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should set Failed phase for invalid governance policy", func() {
			By("Reconciling the invalid resource")
			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the status is Failed")
			var flow flowv1.FoundryFlow
			Expect(k8sClient.Get(ctx, typeNamespacedName, &flow)).To(Succeed())
			Expect(flow.Status.Phase).To(Equal("Failed"))

			readyCond := meta.FindStatusCondition(flow.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("GovernancePolicyInvalid"))
		})
	})

	Context("When reconciling infrastructure", func() {
		const resourceName = "test-flow-infra"
		const testNamespace = "infra-test"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: testNamespace,
		}

		BeforeEach(func() {
			By("creating the test namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: testNamespace},
			}
			var existing corev1.Namespace
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: testNamespace}, &existing); errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, ns)).To(Succeed())
			}

			By("creating a FoundryFlow with EventBusConfig and governance thresholds")
			var existingFlow flowv1.FoundryFlow
			err := k8sClient.Get(ctx, typeNamespacedName, &existingFlow)
			if err != nil && errors.IsNotFound(err) {
				tier1Threshold := resource.MustParse("100")
				tier2Threshold := resource.MustParse("50.5")
				tier1TTL := metav1.Duration{Duration: 24 * time.Hour}
				flowResource := &flowv1.FoundryFlow{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: testNamespace,
					},
					Spec: flowv1.FoundryFlowSpec{
						EntryContracts: map[string]flowv1.Contract{
							"default": {},
						},
						ExitContracts: map[string]flowv1.Contract{
							"default": {},
						},
						GovernancePolicy: flowv1.GovernancePolicy{
							MaxVisits:      10,
							DefaultTimeout: metav1.Duration{Duration: 5 * time.Minute},
							MaxTimeout:     metav1.Duration{Duration: 30 * time.Minute},
							FrictionThresholds: &flowv1.FrictionThresholds{
								Tier1: &tier1Threshold,
								Tier2: &tier2Threshold,
							},
							ReviewTTLs: &flowv1.ReviewTTLs{
								Tier1: &tier1TTL,
							},
						},
						EventBusConfig: &flowv1.EventBusConfig{
							Retention: flowv1.EventBusRetention{
								TelemetryDuration: "24h",
								TelemetrySize:     "100MB",
								AuditDuration:     "168h",
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, flowResource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &flowv1.FoundryFlow{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance FoundryFlow")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			// Clean up infrastructure resources (envtest does not run garbage collection).
			By("Cleanup infrastructure Deployments and Services")
			infraNames := []string{"flow-eventbus", "flow-frictionledger", "flow-monitor", "flow-librarian"}
			for _, name := range infraNames {
				deploy := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: testNamespace}, deploy); err == nil {
					_ = k8sClient.Delete(ctx, deploy)
				}
				svc := &corev1.Service{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: testNamespace}, svc); err == nil {
					_ = k8sClient.Delete(ctx, svc)
				}
			}
		})

		It("should create Event Bus Deployment and Service", func() {
			By("Reconciling the resource")
			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the Event Bus Deployment exists")
			var deploy appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "flow-eventbus",
				Namespace: testNamespace,
			}, &deploy)).To(Succeed())
			Expect(deploy.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(deploy.Spec.Template.Spec.Containers[0].Image).To(Equal(eventBusImage))

			By("Verifying Event Bus retention env vars are set")
			envMap := envVarMap(deploy.Spec.Template.Spec.Containers[0].Env)
			Expect(envMap).To(HaveKeyWithValue("EVENT_BUS_RETENTION_TELEMETRY_DURATION", "24h"))
			Expect(envMap).To(HaveKeyWithValue("EVENT_BUS_RETENTION_TELEMETRY_SIZE", "100MB"))
			Expect(envMap).To(HaveKeyWithValue("EVENT_BUS_RETENTION_AUDIT_DURATION", "168h"))
			Expect(envMap).NotTo(HaveKey("EVENT_BUS_RETENTION_AUDIT_SIZE"))
			Expect(envMap).NotTo(HaveKey("EVENT_BUS_RETENTION_FRICTION_DURATION"))

			By("Verifying the Event Bus Service exists")
			var svc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "flow-eventbus",
				Namespace: testNamespace,
			}, &svc)).To(Succeed())
			Expect(svc.Spec.Ports).To(HaveLen(1))
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(50056)))
		})

		It("should create Friction Ledger Deployment with threshold env vars", func() {
			By("Reconciling the resource")
			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the Friction Ledger Deployment exists")
			var deploy appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "flow-frictionledger",
				Namespace: testNamespace,
			}, &deploy)).To(Succeed())
			Expect(deploy.Spec.Template.Spec.Containers[0].Image).To(Equal(frictionLedgerImage))

			By("Verifying friction threshold env vars")
			envMap := envVarMap(deploy.Spec.Template.Spec.Containers[0].Env)
			Expect(envMap).To(HaveKeyWithValue("EVENT_BUS_ADDRESS", "flow-eventbus:50056"))
			Expect(envMap).To(HaveKey("FRICTION_THRESHOLD_TIER1"))
			Expect(envMap).To(HaveKey("FRICTION_THRESHOLD_TIER2"))
			Expect(envMap).NotTo(HaveKey("FRICTION_THRESHOLD_TIER3"))

			By("Verifying the Friction Ledger Service exists")
			var svc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "flow-frictionledger",
				Namespace: testNamespace,
			}, &svc)).To(Succeed())
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(50057)))
		})

		It("should create Flow Monitor Deployment and Service", func() {
			By("Reconciling the resource")
			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the Flow Monitor Deployment exists")
			var deploy appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "flow-monitor",
				Namespace: testNamespace,
			}, &deploy)).To(Succeed())
			Expect(deploy.Spec.Template.Spec.Containers[0].Image).To(Equal(flowMonitorImage))

			By("Verifying Flow Monitor env vars")
			envMap := envVarMap(deploy.Spec.Template.Spec.Containers[0].Env)
			Expect(envMap).To(HaveKeyWithValue("EVENT_BUS_ADDRESS", "flow-eventbus:50056"))
			Expect(envMap).To(HaveKeyWithValue("FLOW_MONITOR_PORT", "2112"))

			By("Verifying the Flow Monitor Service exists on HTTP port")
			var svc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "flow-monitor",
				Namespace: testNamespace,
			}, &svc)).To(Succeed())
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(2112)))
			Expect(svc.Spec.Ports[0].Name).To(Equal("http-metrics"))
		})

		It("should create Librarian Deployment with review TTL env vars", func() {
			By("Reconciling the resource")
			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the Librarian Deployment exists")
			var deploy appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "flow-librarian",
				Namespace: testNamespace,
			}, &deploy)).To(Succeed())
			Expect(deploy.Spec.Template.Spec.Containers[0].Image).To(Equal(librarianImage))

			By("Verifying Librarian env vars")
			envMap := envVarMap(deploy.Spec.Template.Spec.Containers[0].Env)
			Expect(envMap).To(HaveKeyWithValue("EVENT_BUS_ADDRESS", "flow-eventbus:50056"))
			Expect(envMap).To(HaveKeyWithValue("OPERATOR_ADDRESS", "flow-operator:50052"))
			Expect(envMap).To(HaveKeyWithValue("REVIEW_TTL_TIER1", "24h0m0s"))
			Expect(envMap).NotTo(HaveKey("REVIEW_TTL_TIER2"))

			By("Verifying the Librarian Service exists")
			var svc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "flow-librarian",
				Namespace: testNamespace,
			}, &svc)).To(Succeed())
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(50058)))
		})

		It("should set owner references on infrastructure resources", func() {
			By("Reconciling the resource")
			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying owner references on Event Bus Deployment")
			var deploy appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "flow-eventbus",
				Namespace: testNamespace,
			}, &deploy)).To(Succeed())
			Expect(deploy.OwnerReferences).To(HaveLen(1))
			Expect(deploy.OwnerReferences[0].Name).To(Equal(resourceName))
			Expect(deploy.OwnerReferences[0].Kind).To(Equal("FoundryFlow"))
		})
	})
})

// envVarMap converts a slice of EnvVar to a map for easy assertions.
func envVarMap(envs []corev1.EnvVar) map[string]string {
	m := make(map[string]string, len(envs))
	for _, e := range envs {
		m[e.Name] = e.Value
	}
	return m
}
