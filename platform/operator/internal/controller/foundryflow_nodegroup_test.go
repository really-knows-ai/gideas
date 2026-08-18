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
})
