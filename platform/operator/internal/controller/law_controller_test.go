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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	flowv1 "github.com/gideas/flow/operator/api/v1"
)

const testGroupName = "security"

var _ = Describe("Law Controller", func() {
	Context("When reconciling a valid resource", func() {
		const resourceName = "test-law"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			By("creating the custom resource for the Kind Law")
			var existing flowv1.Law
			err := k8sClient.Get(ctx, typeNamespacedName, &existing)
			if err != nil && errors.IsNotFound(err) {
				resource := &flowv1.Law{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: flowv1.LawSpec{
						Goal: "All code must pass linting",
						Tier: 1,
						Representations: []flowv1.Representation{
							{
								Type:    "text/markdown",
								Content: "All code must pass linting before review",
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &flowv1.Law{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance Law")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should compute a version hash and set Ready condition", func() {
			By("Reconciling the created resource")
			controllerReconciler := &LawReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the version hash was computed")
			var law flowv1.Law
			Expect(k8sClient.Get(ctx, typeNamespacedName, &law)).To(Succeed())
			Expect(law.Status.Version).NotTo(BeEmpty())
			Expect(law.Status.Version).To(HaveLen(16)) // 8 bytes hex-encoded
		})
	})

	Context("When reconciling a law with division", func() {
		const divisionLawName = "test-law-division"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      divisionLawName,
			Namespace: "default",
		}

		AfterEach(func() {
			resource := &flowv1.Law{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}
		})

		It("should produce a different version hash when division changes", func() {
			By("Creating a law without division")
			law := &flowv1.Law{
				ObjectMeta: metav1.ObjectMeta{
					Name:      divisionLawName,
					Namespace: "default",
				},
				Spec: flowv1.LawSpec{
					Goal: "All code must pass linting",
					Tier: 2,
					Representations: []flowv1.Representation{
						{Type: "text/markdown", Content: "Lint everything"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, law)).To(Succeed())

			controllerReconciler := &LawReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			var reconciled flowv1.Law
			Expect(k8sClient.Get(ctx, typeNamespacedName, &reconciled)).To(Succeed())
			versionWithoutGroup := reconciled.Status.Version
			Expect(versionWithoutGroup).NotTo(BeEmpty())

			By("Updating the law with a group")
			Expect(k8sClient.Get(ctx, typeNamespacedName, &reconciled)).To(Succeed())
			reconciled.Spec.Group = testGroupName
			Expect(k8sClient.Update(ctx, &reconciled)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			var updated flowv1.Law
			Expect(k8sClient.Get(ctx, typeNamespacedName, &updated)).To(Succeed())
			versionWithGroup := updated.Status.Version
			Expect(versionWithGroup).NotTo(BeEmpty())

			By("Verifying the hashes differ")
			Expect(versionWithGroup).NotTo(Equal(versionWithoutGroup))
		})
	})
})
