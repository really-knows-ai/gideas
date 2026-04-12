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

			By("creating the intake node used by custom import types")
			var intakeNode flowv1.FoundryNode
			getErr := k8sClient.Get(ctx, types.NamespacedName{Name: "intake-triage", Namespace: testNamespace}, &intakeNode)
			if getErr != nil && errors.IsNotFound(getErr) {
				intakeNode = flowv1.FoundryNode{
					ObjectMeta: metav1.ObjectMeta{Name: "intake-triage", Namespace: testNamespace},
					Spec:       flowv1.FoundryNodeSpec{Image: "triage:latest", Entry: "default"},
				}
				Expect(k8sClient.Create(ctx, &intakeNode)).To(Succeed())
			}

			By("creating a FoundryFlow with EventBusConfig and governance thresholds")
			var existingFlow flowv1.FoundryFlow
			err := k8sClient.Get(ctx, typeNamespacedName, &existingFlow)
			if err != nil && errors.IsNotFound(err) {
				tier1Threshold := resource.MustParse("100")
				tier2Threshold := resource.MustParse("50.5")
				tier1TTL := metav1.Duration{Duration: 24 * time.Hour}
				autoNaturalise := false
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
								"telemetry": {Duration: "24h", Size: "100MB"},
								"audit":     {Duration: "168h"},
							},
						},
						CrossFlow: &flowv1.CrossFlowConfig{
							FederationCA: "-----BEGIN CERTIFICATE-----\nZmFrZS1mZWRlcmF0aW9uLWNh\n-----END CERTIFICATE-----",
							ImportTypes: map[string]flowv1.ImportTypeSpec{
								"external-submission": {Node: "intake-triage"},
							},
							Naturalisation: &flowv1.NaturalisationConfig{
								AutoNaturalise:     &autoNaturalise,
								RequireLocalStamps: []string{"import-reviewed", "import-attested"},
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
			infraNames := []string{"flow-eventbus", "flow-frictionledger", "flow-monitor", "flow-librarian", "flow-embassy"}
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

			intakeNode := &flowv1.FoundryNode{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "intake-triage", Namespace: testNamespace}, intakeNode); err == nil {
				_ = k8sClient.Delete(ctx, intakeNode)
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
			Expect(envMap).To(HaveKey("EVENT_BUS_RETENTION_CONFIG"))

			// Parse the JSON to verify structure.
			var retentionConfig map[string]struct {
				Duration string `json:"duration,omitempty"`
				Size     string `json:"size,omitempty"`
			}
			Expect(json.Unmarshal([]byte(envMap["EVENT_BUS_RETENTION_CONFIG"]), &retentionConfig)).To(Succeed())
			Expect(retentionConfig).To(HaveKey("telemetry"))
			Expect(retentionConfig["telemetry"].Duration).To(Equal("24h"))
			Expect(retentionConfig["telemetry"].Size).To(Equal("100MB"))
			Expect(retentionConfig).To(HaveKey("audit"))
			Expect(retentionConfig["audit"].Duration).To(Equal("168h"))
			Expect(retentionConfig).NotTo(HaveKey("friction"))

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

		It("should create Embassy Deployment and Service", func() {
			By("Reconciling the resource")
			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the Embassy Deployment exists")
			var deploy appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "flow-embassy",
				Namespace: testNamespace,
			}, &deploy)).To(Succeed())
			Expect(deploy.Spec.Template.Spec.Containers[0].Image).To(Equal(embassyImage))

			By("Verifying Embassy env vars")
			envMap := envVarMap(deploy.Spec.Template.Spec.Containers[0].Env)
			Expect(envMap).To(HaveKeyWithValue("EVENT_BUS_ADDRESS", "flow-eventbus:50056"))
			Expect(envMap).To(HaveKeyWithValue("OPERATOR_ADDRESS", "flow-operator:50052"))
			Expect(envMap).To(HaveKeyWithValue("EMBASSY_PORT", "50059"))

			By("Verifying the Embassy Service exists")
			var svc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "flow-embassy",
				Namespace: testNamespace,
			}, &svc)).To(Succeed())
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(50059)))
			Expect(svc.Spec.Ports[0].Name).To(Equal("grpc"))
		})

		It("should project Embassy trust inputs from cross-flow config", func() {
			By("Reconciling the resource")
			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Embassy trust env vars")
			var deploy appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "flow-embassy",
				Namespace: testNamespace,
			}, &deploy)).To(Succeed())

			envMap := envVarMap(deploy.Spec.Template.Spec.Containers[0].Env)
			Expect(envMap).To(HaveKeyWithValue(
				"EMBASSY_FEDERATION_CA_PEM",
				"-----BEGIN CERTIFICATE-----\nZmFrZS1mZWRlcmF0aW9uLWNh\n-----END CERTIFICATE-----",
			))
		})

		It("should project Embassy naturalisation and system import type config", func() {
			By("Reconciling the resource")
			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Embassy naturalisation and import type env vars")
			var deploy appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "flow-embassy",
				Namespace: testNamespace,
			}, &deploy)).To(Succeed())

			envMap := envVarMap(deploy.Spec.Template.Spec.Containers[0].Env)
			Expect(envMap).To(HaveKey("EMBASSY_NATURALISATION_CONFIG"))
			Expect(envMap).To(HaveKey("EMBASSY_SYSTEM_IMPORT_TYPES"))
			Expect(envMap).To(HaveKey("EMBASSY_FLOW_IMPORT_TYPES"))

			var naturalisation struct {
				AutoNaturalise     bool     `json:"autoNaturalise"`
				RequireLocalStamps []string `json:"requireLocalStamps"`
			}
			Expect(json.Unmarshal([]byte(envMap["EMBASSY_NATURALISATION_CONFIG"]), &naturalisation)).To(Succeed())
			Expect(naturalisation.AutoNaturalise).To(BeFalse())
			Expect(naturalisation.RequireLocalStamps).To(ConsistOf("import-reviewed", "import-attested"))

			var systemImportTypes map[string]struct {
				BuiltIn bool `json:"builtIn"`
			}
			Expect(json.Unmarshal([]byte(envMap["EMBASSY_SYSTEM_IMPORT_TYPES"]), &systemImportTypes)).To(Succeed())
			Expect(systemImportTypes).To(HaveKey("law-petition"))
			Expect(systemImportTypes["law-petition"].BuiltIn).To(BeTrue())

			var flowImportTypes map[string]struct {
				Node string `json:"node"`
			}
			Expect(json.Unmarshal([]byte(envMap["EMBASSY_FLOW_IMPORT_TYPES"]), &flowImportTypes)).To(Succeed())
			Expect(flowImportTypes).To(HaveKey("external-submission"))
			Expect(flowImportTypes["external-submission"].Node).To(Equal("intake-triage"))
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

	Context("When NodeGroup validation fails", func() {
		const testNamespace = "nodegroup-test"

		ctx := context.Background()

		BeforeEach(func() {
			By("creating the test namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: testNamespace},
			}
			var existing corev1.Namespace
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: testNamespace}, &existing); errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, ns)).To(Succeed())
			}
		})

		It("should set Degraded when a NodeGroup references a nonexistent node", func() {
			flowName := "nodegroup-missing-node"
			typeNamespacedName := types.NamespacedName{Name: flowName, Namespace: testNamespace}

			// Create a flow with a NodeGroup referencing a node that does not exist.
			flowResource := &flowv1.FoundryFlow{
				ObjectMeta: metav1.ObjectMeta{Name: flowName, Namespace: testNamespace},
				Spec: flowv1.FoundryFlowSpec{
					EntryContracts: map[string]flowv1.Contract{"default": {}},
					ExitContracts:  map[string]flowv1.Contract{"default": {}},
					GovernancePolicy: flowv1.GovernancePolicy{
						MaxVisits:      10,
						DefaultTimeout: metav1.Duration{Duration: 5 * time.Minute},
						MaxTimeout:     metav1.Duration{Duration: 30 * time.Minute},
					},
					NodeGroups: map[string]flowv1.NodeGroup{
						"codification": {
							Nodes: []string{"nonexistent-node"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, flowResource)).To(Succeed())

			defer func() {
				_ = k8sClient.Delete(ctx, flowResource)
			}()

			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			var flow flowv1.FoundryFlow
			Expect(k8sClient.Get(ctx, typeNamespacedName, &flow)).To(Succeed())
			Expect(flow.Status.Phase).To(Equal("Degraded"))

			readyCond := meta.FindStatusCondition(flow.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Reason).To(Equal("NodeGroupValidationFailed"))
		})

		It("should set Degraded when a node belongs to multiple groups", func() {
			flowName := "nodegroup-multi-membership"
			typeNamespacedName := types.NamespacedName{Name: flowName, Namespace: testNamespace}

			// Create nodes first.
			nodeA := &flowv1.FoundryNode{
				ObjectMeta: metav1.ObjectMeta{Name: "shared-node-a", Namespace: testNamespace},
				Spec:       flowv1.FoundryNodeSpec{Image: "shared:latest"},
			}
			Expect(k8sClient.Create(ctx, nodeA)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, nodeA) }()

			nodeB := &flowv1.FoundryNode{
				ObjectMeta: metav1.ObjectMeta{Name: "other-node-b", Namespace: testNamespace},
				Spec:       flowv1.FoundryNodeSpec{Image: "other:latest"},
			}
			Expect(k8sClient.Create(ctx, nodeB)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, nodeB) }()

			flowResource := &flowv1.FoundryFlow{
				ObjectMeta: metav1.ObjectMeta{Name: flowName, Namespace: testNamespace},
				Spec: flowv1.FoundryFlowSpec{
					EntryContracts: map[string]flowv1.Contract{"default": {}},
					ExitContracts:  map[string]flowv1.Contract{"default": {}},
					GovernancePolicy: flowv1.GovernancePolicy{
						MaxVisits:      10,
						DefaultTimeout: metav1.Duration{Duration: 5 * time.Minute},
						MaxTimeout:     metav1.Duration{Duration: 30 * time.Minute},
					},
					NodeGroups: map[string]flowv1.NodeGroup{
						"group-a": {Nodes: []string{"shared-node-a"}},
						"group-b": {Nodes: []string{"shared-node-a", "other-node-b"}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, flowResource)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, flowResource) }()

			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			var flow flowv1.FoundryFlow
			Expect(k8sClient.Get(ctx, typeNamespacedName, &flow)).To(Succeed())
			Expect(flow.Status.Phase).To(Equal("Degraded"))

			readyCond := meta.FindStatusCondition(flow.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Reason).To(Equal("NodeGroupValidationFailed"))
		})

		It("should set Degraded when a node routes outside its group", func() {
			flowName := "nodegroup-routing-leak"
			typeNamespacedName := types.NamespacedName{Name: flowName, Namespace: testNamespace}

			// Create nodes: internalNode routes to outsideNode.
			outsideNode := &flowv1.FoundryNode{
				ObjectMeta: metav1.ObjectMeta{Name: "outside-node", Namespace: testNamespace},
				Spec:       flowv1.FoundryNodeSpec{Image: "outside:latest"},
			}
			Expect(k8sClient.Create(ctx, outsideNode)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, outsideNode) }()

			internalNode := &flowv1.FoundryNode{
				ObjectMeta: metav1.ObjectMeta{Name: "internal-node", Namespace: testNamespace},
				Spec: flowv1.FoundryNodeSpec{
					Image: "internal:latest",
					Outputs: []flowv1.Output{
						{Name: "escape", Target: "outside-node"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, internalNode)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, internalNode) }()

			flowResource := &flowv1.FoundryFlow{
				ObjectMeta: metav1.ObjectMeta{Name: flowName, Namespace: testNamespace},
				Spec: flowv1.FoundryFlowSpec{
					EntryContracts: map[string]flowv1.Contract{"default": {}},
					ExitContracts:  map[string]flowv1.Contract{"default": {}},
					GovernancePolicy: flowv1.GovernancePolicy{
						MaxVisits:      10,
						DefaultTimeout: metav1.Duration{Duration: 5 * time.Minute},
						MaxTimeout:     metav1.Duration{Duration: 30 * time.Minute},
					},
					NodeGroups: map[string]flowv1.NodeGroup{
						"isolated": {Nodes: []string{"internal-node"}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, flowResource)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, flowResource) }()

			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			var flow flowv1.FoundryFlow
			Expect(k8sClient.Get(ctx, typeNamespacedName, &flow)).To(Succeed())
			Expect(flow.Status.Phase).To(Equal("Degraded"))

			readyCond := meta.FindStatusCondition(flow.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Reason).To(Equal("NodeGroupValidationFailed"))
		})

		It("should set Degraded when a group contract references an invalid stamp", func() {
			flowName := "nodegroup-invalid-stamp"
			typeNamespacedName := types.NamespacedName{Name: flowName, Namespace: testNamespace}

			// Create a GovernedArtefact with limited stamp vocabulary.
			ga := &flowv1.GovernedArtefact{
				ObjectMeta: metav1.ObjectMeta{Name: "codification-input", Namespace: testNamespace},
				Spec:       flowv1.GovernedArtefactSpec{Stamps: []string{"validated"}},
			}
			Expect(k8sClient.Create(ctx, ga)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, ga) }()

			groupNode := &flowv1.FoundryNode{
				ObjectMeta: metav1.ObjectMeta{Name: "codify-node", Namespace: testNamespace},
				Spec:       flowv1.FoundryNodeSpec{Image: "codify:latest"},
			}
			Expect(k8sClient.Create(ctx, groupNode)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, groupNode) }()

			flowResource := &flowv1.FoundryFlow{
				ObjectMeta: metav1.ObjectMeta{Name: flowName, Namespace: testNamespace},
				Spec: flowv1.FoundryFlowSpec{
					EntryContracts: map[string]flowv1.Contract{"default": {}},
					ExitContracts:  map[string]flowv1.Contract{"default": {}},
					GovernancePolicy: flowv1.GovernancePolicy{
						MaxVisits:      10,
						DefaultTimeout: metav1.Duration{Duration: 5 * time.Minute},
						MaxTimeout:     metav1.Duration{Duration: 30 * time.Minute},
					},
					NodeGroups: map[string]flowv1.NodeGroup{
						"codification": {
							EntryContracts: map[string]flowv1.Contract{
								"codify-entry": {"codification-input": {"nonexistent-stamp"}},
							},
							Nodes: []string{"codify-node"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, flowResource)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, flowResource) }()

			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			var flow flowv1.FoundryFlow
			Expect(k8sClient.Get(ctx, typeNamespacedName, &flow)).To(Succeed())
			Expect(flow.Status.Phase).To(Equal("Degraded"))

			readyCond := meta.FindStatusCondition(flow.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Reason).To(Equal("NodeGroupValidationFailed"))
		})

		It("should reconcile to Ready with valid NodeGroups", func() {
			flowName := "nodegroup-valid"
			typeNamespacedName := types.NamespacedName{Name: flowName, Namespace: testNamespace}

			// Create two nodes that route within the same group.
			nodeEntry := &flowv1.FoundryNode{
				ObjectMeta: metav1.ObjectMeta{Name: "codify-entry", Namespace: testNamespace},
				Spec: flowv1.FoundryNodeSpec{
					Image: "codify-entry:latest",
					Entry: "default",
					Outputs: []flowv1.Output{
						{Name: "process", Target: "codify-worker"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, nodeEntry)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, nodeEntry) }()

			nodeWorker := &flowv1.FoundryNode{
				ObjectMeta: metav1.ObjectMeta{Name: "codify-worker", Namespace: testNamespace},
				Spec: flowv1.FoundryNodeSpec{
					Image: "codify-worker:latest",
					Exit:  "default",
				},
			}
			Expect(k8sClient.Create(ctx, nodeWorker)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, nodeWorker) }()

			flowResource := &flowv1.FoundryFlow{
				ObjectMeta: metav1.ObjectMeta{Name: flowName, Namespace: testNamespace},
				Spec: flowv1.FoundryFlowSpec{
					EntryContracts: map[string]flowv1.Contract{"default": {}},
					ExitContracts:  map[string]flowv1.Contract{"default": {}},
					GovernancePolicy: flowv1.GovernancePolicy{
						MaxVisits:      10,
						DefaultTimeout: metav1.Duration{Duration: 5 * time.Minute},
						MaxTimeout:     metav1.Duration{Duration: 30 * time.Minute},
					},
					NodeGroups: map[string]flowv1.NodeGroup{
						"codification": {
							Nodes: []string{"codify-entry", "codify-worker"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, flowResource)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, flowResource) }()

			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			var flow flowv1.FoundryFlow
			Expect(k8sClient.Get(ctx, typeNamespacedName, &flow)).To(Succeed())
			Expect(flow.Status.Phase).To(Equal("Ready"))
		})
	})

	Context("When the singleton invariant is violated", func() {
		const testNamespace = "singleton-test"

		ctx := context.Background()

		BeforeEach(func() {
			By("creating the test namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: testNamespace},
			}
			var existing corev1.Namespace
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: testNamespace}, &existing); errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, ns)).To(Succeed())
			}
		})

		It("should set Degraded when multiple FoundryFlows exist in the same namespace", func() {
			flow1Name := "flow-one"
			flow2Name := "flow-two"

			minimalSpec := flowv1.FoundryFlowSpec{
				EntryContracts: map[string]flowv1.Contract{"default": {}},
				ExitContracts:  map[string]flowv1.Contract{"default": {}},
				GovernancePolicy: flowv1.GovernancePolicy{
					MaxVisits:      10,
					DefaultTimeout: metav1.Duration{Duration: 5 * time.Minute},
					MaxTimeout:     metav1.Duration{Duration: 30 * time.Minute},
				},
			}

			flow1 := &flowv1.FoundryFlow{
				ObjectMeta: metav1.ObjectMeta{Name: flow1Name, Namespace: testNamespace},
				Spec:       minimalSpec,
			}
			Expect(k8sClient.Create(ctx, flow1)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, flow1) }()

			flow2 := &flowv1.FoundryFlow{
				ObjectMeta: metav1.ObjectMeta{Name: flow2Name, Namespace: testNamespace},
				Spec:       minimalSpec,
			}
			Expect(k8sClient.Create(ctx, flow2)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, flow2) }()

			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("Reconciling the first flow — should be Degraded")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: flow1Name, Namespace: testNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var got1 flowv1.FoundryFlow
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: flow1Name, Namespace: testNamespace}, &got1)).To(Succeed())
			Expect(got1.Status.Phase).To(Equal("Degraded"))

			readyCond := meta.FindStatusCondition(got1.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("SingletonViolation"))

			By("Reconciling the second flow — should also be Degraded")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: flow2Name, Namespace: testNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var got2 flowv1.FoundryFlow
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: flow2Name, Namespace: testNamespace}, &got2)).To(Succeed())
			Expect(got2.Status.Phase).To(Equal("Degraded"))

			readyCond2 := meta.FindStatusCondition(got2.Status.Conditions, "Ready")
			Expect(readyCond2).NotTo(BeNil())
			Expect(readyCond2.Reason).To(Equal("SingletonViolation"))
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
