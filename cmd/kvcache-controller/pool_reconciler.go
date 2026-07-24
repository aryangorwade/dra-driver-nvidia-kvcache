/*
Copyright The Kubernetes Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	nvapi "sigs.k8s.io/dra-driver-nvidia-gpu/api/nvidia.com/resource/v1beta1"
)

const (
	// kvcacheFinalizer blocks KVCachePool deletion until the controller has
	// removed the pool's LMCache children. A cluster-scoped pool cannot own
	// namespaced objects via ownerReferences, so cleanup is finalizer-driven.
	kvcacheFinalizer = "kvcache.nvidia.com/pool-cleanup"

	// kvcachePoolNameLabelKey labels LMCache children (Deployment/Service)
	// with the KVCachePool they back.
	kvcachePoolNameLabelKey = "kvcache.nvidia.com/pool-name"
)

// PoolReconciler reconciles a KVCachePool into an LMCache Deployment and
// Service, and publishes the data-plane address in status.endpoint once ready.
type PoolReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Namespace is where LMCache children are created (e.g. kvcache-system).
	Namespace string
	// LMCacheImage is the image used for provisioned LMCache Deployments.
	LMCacheImage string
	// Port is the LMCache service port used to build status.endpoint.
	Port int32
}

// +kubebuilder:rbac:groups=resource.nvidia.com,resources=kvcachepools,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=resource.nvidia.com,resources=kvcachepools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

func (r *PoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var pool nvapi.KVCachePool
	if err := r.Get(ctx, req.NamespacedName, &pool); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !pool.DeletionTimestamp.IsZero() {
		if err := r.deleteLMCacheChildren(ctx, &pool); err != nil {
			return ctrl.Result{}, err
		}
		if controllerutil.RemoveFinalizer(&pool, kvcacheFinalizer) {
			return ctrl.Result{}, r.Update(ctx, &pool)
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&pool, kvcacheFinalizer) {
		if err := r.Update(ctx, &pool); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.ensureLMCacheChildren(ctx, &pool); err != nil {
		return ctrl.Result{}, err
	}

	ready, err := r.lmcacheReady(ctx, &pool)
	if err != nil {
		return ctrl.Result{}, err
	}

	newStatus := pool.Status.DeepCopy()
	newStatus.Status = nvapi.KVCachePoolStatusNotReady
	newStatus.Endpoint = ""
	if ready {
		newStatus.Status = nvapi.KVCachePoolStatusReady
		newStatus.Endpoint = fmt.Sprintf("lmcache-%s.%s.svc.cluster.local:%d", pool.Name, r.Namespace, r.Port)
	}
	if *newStatus == pool.Status {
		return ctrl.Result{}, nil
	}
	pool.Status = *newStatus
	return ctrl.Result{}, r.Status().Update(ctx, &pool)
}

// ensureLMCacheChildren creates or updates the LMCache Deployment and Service
// backing the pool, labelled with kvcachePoolNameLabelKey.
//
// TODO(step 5): implement typed constructors + controllerutil.CreateOrUpdate.
func (r *PoolReconciler) ensureLMCacheChildren(ctx context.Context, pool *nvapi.KVCachePool) error {
	return fmt.Errorf("ensureLMCacheChildren not implemented")
}

// deleteLMCacheChildren removes the pool's labelled Deployment and Service.
//
// TODO(step 6): implement label-based deletion before finalizer removal.
func (r *PoolReconciler) deleteLMCacheChildren(ctx context.Context, pool *nvapi.KVCachePool) error {
	return fmt.Errorf("deleteLMCacheChildren not implemented")
}

// lmcacheReady reports whether the pool's LMCache Deployment is available and
// its Service exists.
//
// TODO(step 5): implement readiness from Deployment status + Service presence.
func (r *PoolReconciler) lmcacheReady(ctx context.Context, pool *nvapi.KVCachePool) (bool, error) {
	return false, nil
}

// poolForChild maps an LMCache child object event back to its KVCachePool.
func (r *PoolReconciler) poolForChild(ctx context.Context, obj client.Object) []ctrl.Request {
	poolName := obj.GetLabels()[kvcachePoolNameLabelKey]
	if poolName == "" {
		return nil
	}
	return []ctrl.Request{{NamespacedName: types.NamespacedName{Name: poolName}}}
}

func (r *PoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&nvapi.KVCachePool{}).
		Watches(&appsv1.Deployment{}, handler.EnqueueRequestsFromMapFunc(r.poolForChild)).
		Complete(r)
}
