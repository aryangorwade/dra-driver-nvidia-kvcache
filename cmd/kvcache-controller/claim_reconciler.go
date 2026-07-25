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

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nvapi "sigs.k8s.io/dra-driver-nvidia-gpu/api/nvidia.com/resource/v1beta1"
)

const (
	// DriverName is the DRA driver this controller serves. Must match the
	// driver name used by the kvcache kubelet plugin.
	DriverName = "kvcache.nvidia.com"
)

// ClaimReconciler binds ResourceClaims that carry a KVCachePoolConfig to a
// KVCachePool (match-or-provision, see plans/v0.md). The chosen pool name is
// recorded on the claim via nvapi.KVCachePoolClaimAnnotationKey, which the
// kubelet plugin consumes at Prepare time.
type ClaimReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=resource.k8s.io,resources=resourceclaims,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=resource.nvidia.com,resources=kvcachepools,verbs=get;list;watch;create;update;patch

func (r *ClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var claim resourceapi.ResourceClaim
	if err := r.Get(ctx, req.NamespacedName, &claim); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	cfg, err := decodeKVCachePoolConfig(&claim) // scan spec.devices.config for driver kvcache.nvidia.com
	if err != nil {
		// Permanent: malformed/invalid claim config will not fix itself on
		// retry. Log and stop; returning an error would requeue forever.
		ctrl.LoggerFrom(ctx).Error(err, "invalid KVCachePoolConfig on ResourceClaim; not binding",
			"claim", req.NamespacedName)
		return ctrl.Result{}, nil
	}
	if cfg == nil {
		return ctrl.Result{}, nil // claim does not target kvcache.nvidia.com
	}
	if claim.Annotations[nvapi.KVCachePoolClaimAnnotationKey] != "" {
		return ctrl.Result{}, nil // already bound
	}

	pool, err := r.matchOrProvision(ctx, cfg)
	if err != nil {
		return ctrl.Result{}, err // requeued with backoff automatically
	}

	patch := client.MergeFrom(claim.DeepCopy())
	if claim.Annotations == nil {
		claim.Annotations = map[string]string{}
	}
	claim.Annotations[nvapi.KVCachePoolClaimAnnotationKey] = pool.Name
	return ctrl.Result{}, r.Patch(ctx, &claim, patch)
}

// decodeKVCachePoolConfig scans the claim's spec-level opaque device configs
// for one addressed to this driver and decodes it as a KVCachePoolConfig.
// Returns (nil, nil) when the claim does not target this driver.
func decodeKVCachePoolConfig(claim *resourceapi.ResourceClaim) (*nvapi.KVCachePoolConfig, error) {
	var found *nvapi.KVCachePoolConfig
	for _, c := range claim.Spec.Devices.Config {
		if c.Opaque == nil || c.Opaque.Driver != DriverName {
			continue
		}
		obj, err := runtime.Decode(nvapi.StrictDecoder, c.Opaque.Parameters.Raw)
		if err != nil {
			return nil, fmt.Errorf("decoding opaque parameters: %w", err)
		}
		cfg, ok := obj.(*nvapi.KVCachePoolConfig)
		if !ok {
			continue // non KVCachePoolCOnfig kind in our API group
		}
		found = cfg // later entries win
	}
	if found == nil {
		return nil, nil
	}
	if err := found.Normalize(); err != nil {
		return nil, err
	}
	if err := found.Validate(); err != nil {
		return nil, err
	}
	return found, nil
}

// matchOrProvision returns the KVCachePool for the given config: the exact
// pool when cfg.PoolName is set, an existing pool matching the five
// compatibility keys, or a newly created pool with a deterministic name.
//
// TODO(step 3): implement match-or-provision.
func (r *ClaimReconciler) matchOrProvision(ctx context.Context, cfg *nvapi.KVCachePoolConfig) (*nvapi.KVCachePool, error) {
	return nil, fmt.Errorf("matchOrProvision not implemented")
}

func (r *ClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&resourceapi.ResourceClaim{}).
		Complete(r)
}
