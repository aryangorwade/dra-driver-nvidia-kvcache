package main

import (
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	nvapi "sigs.k8s.io/dra-driver-nvidia-gpu/api/nvidia.com/resource/v1beta1"
)

var scheme = runtime.NewScheme()

func init() {
	_ = clientgoscheme.AddToScheme(scheme) // core, apps, resource.k8s.io
	_ = nvapi.AddToScheme(scheme)          // KVCachePool
}

func main() {
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		LeaderElection:         true,
		LeaderElectionID:       "kvcache-controller.nvidia.com",
		HealthProbeBindAddress: ":8081",
	})
	if err != nil {
		os.Exit(1)
	}

	// Reconciler to manage KVCacheClaim objects.
	if err := (&ClaimReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme()}).SetupWithManager(mgr); err != nil {
		os.Exit(1)
	}

	// Reconciler to create/manage actual pool infrastructure, backed by LMCache.
	// TODO: Beyond v0, add more data providers.
	if err := (&PoolReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(),
		Namespace: "kvcache-system", LMCacheImage: os.Getenv("LMCACHE_IMAGE")}).SetupWithManager(mgr); err != nil {
		os.Exit(1)
	}

	_ = mgr.AddHealthzCheck("healthz", healthz.Ping)
	_ = mgr.AddReadyzCheck("readyz", healthz.Ping)

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		os.Exit(1)
	}
}
