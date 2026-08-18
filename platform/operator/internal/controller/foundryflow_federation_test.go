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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

var _ = Describe("FoundryFlow Controller", func() {
	Context("When projecting federation config to Embassy", func() {
		const resourceName = "test-flow-fed"
		const testNamespace = "fed-proj-test"

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

			By("creating a FoundryFlow with federation config")
			var existingFlow flowv1.FoundryFlow
			err := k8sClient.Get(ctx, typeNamespacedName, &existingFlow)
			if err != nil && errors.IsNotFound(err) {
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
						},
						CrossFlow: &flowv1.CrossFlowConfig{
							FederationCA: "-----BEGIN CERTIFICATE-----\nZmFrZS1mZWRlcmF0aW9uLWNh\n-----END CERTIFICATE-----",
							Federation: &flowv1.FederationConfig{
								Identity:           "flow-alpha",
								States:             []string{"california", "nevada"},
								FederationEndpoint: "federation.example.com:50061",
								PublisherRoles: []flowv1.FederationPublisherRole{
									{Scope: "security", Level: "state"},
								},
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

			By("Cleanup infrastructure Deployments and Services")
			infraNames := []string{"flow-eventbus", "flow-frictionledger", "flow-monitor", "flow-librarian", "flow-embassy", "flow-federation"}
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

		It("should project federation identity, endpoint, and states to Embassy env vars", func() {
			By("Reconciling the resource")
			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Embassy federation env vars")
			var deploy appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "flow-embassy",
				Namespace: testNamespace,
			}, &deploy)).To(Succeed())

			envMap := envVarMap(deploy.Spec.Template.Spec.Containers[0].Env)

			By("Verifying EMBASSY_FEDERATION_IDENTITY")
			Expect(envMap).To(HaveKeyWithValue("EMBASSY_FEDERATION_IDENTITY", "flow-alpha"))

			By("Verifying EMBASSY_FEDERATION_ENDPOINT")
			Expect(envMap).To(HaveKeyWithValue("EMBASSY_FEDERATION_ENDPOINT", "federation.example.com:50061"))

			By("Verifying EMBASSY_FEDERATION_STATES is JSON-encoded list")
			Expect(envMap).To(HaveKey("EMBASSY_FEDERATION_STATES"))
			var states []string
			Expect(json.Unmarshal([]byte(envMap["EMBASSY_FEDERATION_STATES"]), &states)).To(Succeed())
			Expect(states).To(ConsistOf("california", "nevada"))

			By("Verifying existing EMBASSY_FEDERATION_CA_PEM is still set")
			Expect(envMap).To(HaveKeyWithValue(
				"EMBASSY_FEDERATION_CA_PEM",
				"-----BEGIN CERTIFICATE-----\nZmFrZS1mZWRlcmF0aW9uLWNh\n-----END CERTIFICATE-----",
			))

			By("Verifying FEDERATION_ADDRESS is projected")
			Expect(envMap).To(HaveKeyWithValue("FEDERATION_ADDRESS", "flow-federation:50061"))
		})
	})

	Context("When reconciling Federation infrastructure", func() {
		const resourceName = "test-flow-federation"
		const testNamespace = "federation-infra-test"

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
		})

		AfterEach(func() {
			By("Cleanup infrastructure Deployments and Services")
			infraNames := []string{"flow-eventbus", "flow-frictionledger", "flow-monitor", "flow-librarian", "flow-embassy", "flow-federation"}
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

		It("should create Federation Deployment and Service when federation is set", func() {
			By("creating a FoundryFlow with federation config")
			flowResource := &flowv1.FoundryFlow{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: testNamespace,
				},
				Spec: flowv1.FoundryFlowSpec{
					EntryContracts: map[string]flowv1.Contract{"default": {}},
					ExitContracts:  map[string]flowv1.Contract{"default": {}},
					GovernancePolicy: flowv1.GovernancePolicy{
						MaxVisits:      10,
						DefaultTimeout: metav1.Duration{Duration: 5 * time.Minute},
						MaxTimeout:     metav1.Duration{Duration: 30 * time.Minute},
					},
					CrossFlow: &flowv1.CrossFlowConfig{
						Federation: &flowv1.FederationConfig{
							Identity:           "flow-alpha",
							States:             []string{"california"},
							FederationEndpoint: "federation.example.com:50061",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, flowResource)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, flowResource) }()

			By("Reconciling the resource")
			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the Federation Deployment exists with correct image and port")
			var deploy appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "flow-federation",
				Namespace: testNamespace,
			}, &deploy)).To(Succeed())
			Expect(deploy.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(deploy.Spec.Template.Spec.Containers[0].Image).To(Equal("flow-federation:latest"))
			Expect(deploy.Spec.Template.Spec.Containers[0].Ports).To(HaveLen(1))
			Expect(deploy.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort).To(Equal(int32(50061)))

			By("Verifying Federation labels")
			Expect(deploy.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", "flow-federation"))
			Expect(deploy.Labels).To(HaveKeyWithValue("app.kubernetes.io/component", "control-plane"))

			By("Verifying the Federation Service exists")
			var svc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "flow-federation",
				Namespace: testNamespace,
			}, &svc)).To(Succeed())
			Expect(svc.Spec.Ports).To(HaveLen(1))
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(50061)))
			Expect(svc.Spec.Ports[0].Name).To(Equal("grpc"))
		})

		It("should set Federation env vars including FEDERATION_PORT and FEDERATION_NAMESPACE", func() {
			By("creating a FoundryFlow with federation config")
			flowResource := &flowv1.FoundryFlow{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: testNamespace,
				},
				Spec: flowv1.FoundryFlowSpec{
					EntryContracts: map[string]flowv1.Contract{"default": {}},
					ExitContracts:  map[string]flowv1.Contract{"default": {}},
					GovernancePolicy: flowv1.GovernancePolicy{
						MaxVisits:      10,
						DefaultTimeout: metav1.Duration{Duration: 5 * time.Minute},
						MaxTimeout:     metav1.Duration{Duration: 30 * time.Minute},
					},
					CrossFlow: &flowv1.CrossFlowConfig{
						Federation: &flowv1.FederationConfig{
							Identity:           "flow-beta",
							States:             []string{"nevada"},
							FederationEndpoint: "federation.example.com:50061",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, flowResource)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, flowResource) }()

			By("Reconciling the resource")
			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Federation env vars")
			var deploy appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "flow-federation",
				Namespace: testNamespace,
			}, &deploy)).To(Succeed())

			envMap := envVarMap(deploy.Spec.Template.Spec.Containers[0].Env)
			Expect(envMap).To(HaveKeyWithValue("FEDERATION_PORT", "50061"))
			Expect(envMap).To(HaveKeyWithValue("FEDERATION_NAMESPACE", testNamespace))
		})

		It("should not have /data volume or FEDERATION_DB_PATH", func() {
			By("creating a FoundryFlow with federation config")
			flowResource := &flowv1.FoundryFlow{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: testNamespace,
				},
				Spec: flowv1.FoundryFlowSpec{
					EntryContracts: map[string]flowv1.Contract{"default": {}},
					ExitContracts:  map[string]flowv1.Contract{"default": {}},
					GovernancePolicy: flowv1.GovernancePolicy{
						MaxVisits:      10,
						DefaultTimeout: metav1.Duration{Duration: 5 * time.Minute},
						MaxTimeout:     metav1.Duration{Duration: 30 * time.Minute},
					},
					CrossFlow: &flowv1.CrossFlowConfig{
						Federation: &flowv1.FederationConfig{
							Identity:           "flow-gamma",
							States:             []string{"oregon"},
							FederationEndpoint: "federation.example.com:50061",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, flowResource)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, flowResource) }()

			By("Reconciling the resource")
			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying no /data volume or FEDERATION_DB_PATH")
			var deploy appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "flow-federation",
				Namespace: testNamespace,
			}, &deploy)).To(Succeed())

			Expect(deploy.Spec.Template.Spec.Volumes).To(BeEmpty())
			Expect(deploy.Spec.Template.Spec.Containers[0].VolumeMounts).To(BeEmpty())

			envMap := envVarMap(deploy.Spec.Template.Spec.Containers[0].Env)
			Expect(envMap).NotTo(HaveKey("FEDERATION_DB_PATH"))
		})

		It("should not create Federation Deployment or Service when federation is nil", func() {
			By("creating a FoundryFlow without federation config")
			noFedName := "test-flow-no-fed"
			noFedNamespacedName := types.NamespacedName{Name: noFedName, Namespace: testNamespace}

			flowResource := &flowv1.FoundryFlow{
				ObjectMeta: metav1.ObjectMeta{
					Name:      noFedName,
					Namespace: testNamespace,
				},
				Spec: flowv1.FoundryFlowSpec{
					EntryContracts: map[string]flowv1.Contract{"default": {}},
					ExitContracts:  map[string]flowv1.Contract{"default": {}},
					GovernancePolicy: flowv1.GovernancePolicy{
						MaxVisits:      10,
						DefaultTimeout: metav1.Duration{Duration: 5 * time.Minute},
						MaxTimeout:     metav1.Duration{Duration: 30 * time.Minute},
					},
				},
			}
			Expect(k8sClient.Create(ctx, flowResource)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, flowResource) }()

			By("Reconciling the resource")
			controllerReconciler := &FoundryFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: noFedNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying no Federation Deployment exists")
			var deploy appsv1.Deployment
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      "flow-federation",
				Namespace: testNamespace,
			}, &deploy)
			Expect(errors.IsNotFound(err)).To(BeTrue())

			By("Verifying no Federation Service exists")
			var svc corev1.Service
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      "flow-federation",
				Namespace: testNamespace,
			}, &svc)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})
	})
})
