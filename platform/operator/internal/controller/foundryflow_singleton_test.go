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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

var _ = Describe("FoundryFlow Controller", func() {
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
