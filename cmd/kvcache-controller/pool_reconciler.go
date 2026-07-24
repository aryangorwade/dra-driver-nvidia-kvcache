func (r *PoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var pool nvapi.KVCachePool
	if err := r.Get(ctx, req.NamespacedName, &pool); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !pool.DeletionTimestamp.IsZero() {
		// delete labelled Deployment/Service in r.Namespace, then:
		controllerutil.RemoveFinalizer(&pool, kvcacheFinalizer)
		return ctrl.Result{}, r.Update(ctx, &pool)
	}

	// Ensure it has the finalizer
	if controllerutil.AddFinalizer(&pool, kvcacheFinalizer) {
		if err := r.Update(ctx, &pool); err != nil {
			return ctrl.Result{}, err
		}
	}

	// CreateOrUpdate LMCache Deployment + Service (labelled kvcache.nvidia.com/pool-name)
	// Remember: cluster-scoped pool cannot own namespaced children — labels + finalizer, no ownerRef.

	ready := deploymentReady && serviceExists
	pool.Status.Status = nvapi.KVCachePoolStatusNotReady
	if ready {
		pool.Status.Status = nvapi.KVCachePoolStatusReady
		pool.Status.Endpoint = fmt.Sprintf("lmcache-%s.%s.svc.cluster.local:%d", pool.Name, r.Namespace, r.Port)
	}
	return ctrl.Result{}, r.Status().Update(ctx, &pool)
}

func (r *PoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&nvapi.KVCachePool{}).
		Watches(&appsv1.Deployment{}, handler.EnqueueRequestsFromMapFunc(r.poolForChild)).
		Complete(r)
}