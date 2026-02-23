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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
})
