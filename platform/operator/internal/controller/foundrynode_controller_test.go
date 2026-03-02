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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	flowv1 "github.com/gideas/flow/operator/api/v1"
)

var _ = Describe("FoundryNode Controller", func() {
	Context("When reconciling a valid resource", func() {
		const resourceName = "test-node"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			By("creating the custom resource for the Kind FoundryNode")
			var existing flowv1.FoundryNode
			err := k8sClient.Get(ctx, typeNamespacedName, &existing)
			if err != nil && errors.IsNotFound(err) {
				resource := &flowv1.FoundryNode{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: flowv1.FoundryNodeSpec{
						Image: "test-image:latest",
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &flowv1.FoundryNode{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance FoundryNode")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			// Clean up the owned Deployment.
			deploy := &appsv1.Deployment{}
			err = k8sClient.Get(ctx, typeNamespacedName, deploy)
			if err == nil {
				Expect(k8sClient.Delete(ctx, deploy)).To(Succeed())
			}
		})

		It("should create a Deployment and set Ready condition", func() {
			By("Reconciling the created resource")
			controllerReconciler := &FoundryNodeReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the Deployment was created")
			var deploy appsv1.Deployment
			Expect(k8sClient.Get(ctx, typeNamespacedName, &deploy)).To(Succeed())
			Expect(deploy.Spec.Template.Spec.Containers).To(HaveLen(2))
			Expect(deploy.Spec.Template.Spec.Containers[0].Name).To(Equal("node"))
			Expect(deploy.Spec.Template.Spec.Containers[0].Image).To(Equal("test-image:latest"))
			Expect(deploy.Spec.Template.Spec.Containers[1].Name).To(Equal("sidecar"))

			By("Verifying node container env vars")
			nodeEnv := deploy.Spec.Template.Spec.Containers[0].Env
			Expect(nodeEnv).To(ContainElement(corev1.EnvVar{Name: "FLOW_NAMESPACE", Value: "default"}))
			Expect(nodeEnv).To(ContainElement(corev1.EnvVar{Name: "FLOW_NODE_NAME", Value: resourceName}))
			Expect(nodeEnv).To(ContainElement(corev1.EnvVar{Name: "EVENT_BUS_ADDRESS", Value: "flow-eventbus:50056"}))

			By("Verifying sidecar container env vars")
			sidecarEnv := deploy.Spec.Template.Spec.Containers[1].Env
			Expect(sidecarEnv).To(ContainElement(corev1.EnvVar{Name: "FLOW_NAMESPACE", Value: "default"}))
			Expect(sidecarEnv).To(ContainElement(corev1.EnvVar{Name: "EVENT_BUS_ADDRESS", Value: "flow-eventbus:50056"}))

			By("Verifying the Ready condition is set")
			var node flowv1.FoundryNode
			Expect(k8sClient.Get(ctx, typeNamespacedName, &node)).To(Succeed())

			readyCond := meta.FindStatusCondition(node.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(readyCond.Reason).To(Equal("Reconciled"))
		})
	})

	Context("When capabilities are invalid", func() {
		const resourceName = "test-node-invalid-cap"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			By("creating a FoundryNode with invalid capability syntax")
			var existing flowv1.FoundryNode
			err := k8sClient.Get(ctx, typeNamespacedName, &existing)
			if err != nil && errors.IsNotFound(err) {
				resource := &flowv1.FoundryNode{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: flowv1.FoundryNodeSpec{
						Image:        "test-image:latest",
						Capabilities: []string{"INVALID_CAP"},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &flowv1.FoundryNode{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance FoundryNode")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should set Ready=False for invalid capability", func() {
			By("Reconciling the invalid resource")
			controllerReconciler := &FoundryNodeReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the Ready condition is False")
			var node flowv1.FoundryNode
			Expect(k8sClient.Get(ctx, typeNamespacedName, &node)).To(Succeed())

			readyCond := meta.FindStatusCondition(node.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("InvalidCapability"))
		})
	})
})
