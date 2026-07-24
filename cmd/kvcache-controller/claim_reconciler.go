type ClaimReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *ClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var claim resourceapi.ResourceClaim
	if err := r.Get(ctx, req.NamespacedName, &claim); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	cfg := decodeKVCachePoolConfig(&claim) // scan spec.devices.config for driver kvcache.nvidia.com
	if cfg == nil {
		return ctrl.Result{}, nil // does not contain kvcache.nvidia.com
	}
	if claim.Annotations[nvapi.KVCachePoolClaimAnnotationKey] != "" {
		return ctrl.Result{}, nil // already bound
	}

	pool, err := r.matchOrProvision(ctx, cfg) // Get by poolName, or List+match 5 keys (engine, modelFamily, etc), or Create
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

func (r *ClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&resourceapi.ResourceClaim{}).
		Complete(r)
}