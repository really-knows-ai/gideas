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

	appsv1 "k8s.io/api/apps/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	flowv1 "github.com/gideas/flow/operator/api/v1"
)

// FlowSupportServiceReconciler reconciles a FlowSupportService object.
type FlowSupportServiceReconciler struct {
	ServiceReconciler
}

// +kubebuilder:rbac:groups=flow.gideas.io,resources=flowsupportservices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=flow.gideas.io,resources=flowsupportservices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=flow.gideas.io,resources=flowsupportservices/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete

func (r *FlowSupportServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return r.ServiceReconciler.Reconcile(ctx, req, func() ServiceObject { return &flowv1.FlowSupportService{} })
}

func (r *FlowSupportServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&flowv1.FlowSupportService{}).
		Owns(&appsv1.Deployment{}).
		Owns(&appsv1.StatefulSet{}).
		Named("flowsupportservice").
		Complete(r)
}
