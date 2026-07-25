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
	"crypto/sha256"
	"fmt"

	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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

	cfg, err := decodeKVCachePoolConfig(&claim)
	if err != nil {
		// TerminalError is logged/metrics'd by controller-runtime and is not requeued.
		return ctrl.Result{}, err
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
			return nil, reconcile.TerminalError(fmt.Errorf("decoding opaque parameters: %w", err))
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
		return nil, reconcile.TerminalError(fmt.Errorf("normalize KVCachePoolConfig: %w", err))
	}
	if err := found.Validate(); err != nil {
		return nil, reconcile.TerminalError(fmt.Errorf("invalid KVCachePoolConfig: %w", err))
	}
	return found, nil
}

// matchOrProvision returns the KVCachePool for the given config: the exact
// pool when cfg.PoolName is set, otherwise a pool named from the five
// compatibility keys. Missing pools are created (get-or-create).
func (r *ClaimReconciler) matchOrProvision(ctx context.Context, cfg *nvapi.KVCachePoolConfig) (*nvapi.KVCachePool, error) {
	name := cfg.PoolName
	if name == "" {
		name = deterministicPoolName(cfg)
	}
	return r.getOrCreatePool(ctx, name, cfg)
}

// deterministicPoolName hashes the five compatibility fields into a stable
// DNS-1123 subdomain. capacityBytes is intentionally excluded so claims that
// differ only in requested capacity share one pool.
func deterministicPoolName(cfg *nvapi.KVCachePoolConfig) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%s",
		cfg.Engine, cfg.ModelFamily, cfg.Dtype, cfg.BlockSizeTokens, cfg.LatencyTier)))
	return fmt.Sprintf("kvcache-%s-%x", cfg.Engine, sum[:8])
}

func (r *ClaimReconciler) getOrCreatePool(ctx context.Context, name string, cfg *nvapi.KVCachePoolConfig) (*nvapi.KVCachePool, error) {
	var pool nvapi.KVCachePool
	err := r.Get(ctx, client.ObjectKey{Name: name}, &pool)
	if err == nil {
		if !poolMatchesConfig(&pool, cfg) {
			return nil, reconcile.TerminalError(fmt.Errorf(
				"KVCachePool %q exists but is incompatible with claim config (engine=%s modelFamily=%s dtype=%s blockSizeTokens=%d latencyTier=%s)",
				name, cfg.Engine, cfg.ModelFamily, cfg.Dtype, cfg.BlockSizeTokens, cfg.LatencyTier,
			))
		}
		return &pool, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get KVCachePool %q: %w", name, err)
	}

	pool = nvapi.KVCachePool{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: nvapi.KVCachePoolSpec{
			CapacityBytes:   cfg.CapacityBytes,
			LatencyTier:     cfg.LatencyTier,
			Engine:          cfg.Engine,
			ModelFamily:     cfg.ModelFamily,
			Dtype:           cfg.Dtype,
			BlockSizeTokens: cfg.BlockSizeTokens,
		},
	}
	if err := r.Create(ctx, &pool); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Lost the create race; re-get and validate.
			if getErr := r.Get(ctx, client.ObjectKey{Name: name}, &pool); getErr != nil {
				return nil, fmt.Errorf("get KVCachePool %q after create race: %w", name, getErr)
			}
			if !poolMatchesConfig(&pool, cfg) {
				return nil, reconcile.TerminalError(fmt.Errorf(
					"KVCachePool %q exists but is incompatible with claim config", name,
				))
			}
			return &pool, nil
		}
		return nil, fmt.Errorf("create KVCachePool %q: %w", name, err)
	}
	return &pool, nil
}

func poolMatchesConfig(pool *nvapi.KVCachePool, cfg *nvapi.KVCachePoolConfig) bool {
	return pool.Spec.Engine == cfg.Engine &&
		pool.Spec.ModelFamily == cfg.ModelFamily &&
		pool.Spec.Dtype == cfg.Dtype &&
		pool.Spec.BlockSizeTokens == cfg.BlockSizeTokens &&
		pool.Spec.LatencyTier == cfg.LatencyTier
}

func (r *ClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&resourceapi.ResourceClaim{}).
		Complete(r)
}
