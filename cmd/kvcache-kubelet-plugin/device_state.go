/*
Copyright The Kubernetes Authors.

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

package main

import (
	"context"
	"fmt"
	"sync"

	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"

	configapi "sigs.k8s.io/dra-driver-nvidia-gpu/api/nvidia.com/resource/v1beta1"
	nvclientset "sigs.k8s.io/dra-driver-nvidia-gpu/pkg/nvidia.com/clientset/versioned"
)

type OpaqueDeviceConfig struct {
	Requests []string
	Config   runtime.Object
}

type DeviceState struct {
	sync.Mutex
	cdi      *CDIHandler
	config   *Config
	nvclient nvclientset.Interface
}

func NewDeviceState(ctx context.Context, config *Config) (*DeviceState, error) {
	cdi, err := NewCDIHandler(
		WithCDIRoot(config.flags.cdiRoot),
		WithVendor(cdiVendor),
	)
	if err != nil {
		return nil, fmt.Errorf("unable to create CDI handler: %w", err)
	}

	state := &DeviceState{
		cdi:      cdi,
		config:   config,
		nvclient: config.clientsets.Nvidia,
	}

	return state, nil
}

func (s *DeviceState) Prepare(ctx context.Context, claim *resourceapi.ResourceClaim) ([]kubeletplugin.Device, error) {
	s.Lock()
	defer s.Unlock()

	claimUID := string(claim.UID)

	if claim.Status.Allocation == nil {
		return nil, fmt.Errorf("no allocation set in ResourceClaim %s", ResourceClaimToString(claim))
	}

	preparedDevices, err := s.prepareDevices(ctx, claim)
	if err != nil {
		return nil, fmt.Errorf("prepare devices failed: %w", err)
	}

	if err := s.cdi.CreateClaimSpecFile(claimUID, preparedDevices); err != nil {
		return nil, fmt.Errorf("unable to create CDI spec file for claim: %w", err)
	}

	return preparedDevices.GetDevices(), nil
}

func (s *DeviceState) Unprepare(ctx context.Context, claimRef kubeletplugin.NamespacedObject) error {
	s.Lock()
	defer s.Unlock()

	klog.V(6).Infof("Unprepare() for claim '%s'", claimRef.String())

	claimUID := string(claimRef.UID)
	if err := s.cdi.DeleteClaimSpecFileIfExists(claimUID); err != nil {
		return fmt.Errorf("unable to delete CDI spec file for claim: %w", err)
	}

	return nil
}

func (s *DeviceState) prepareDevices(ctx context.Context, claim *resourceapi.ResourceClaim) (PreparedDevices, error) {
	configs, err := GetOpaqueDeviceConfigs(
		configapi.StrictDecoder,
		DriverName,
		claim.Status.Allocation.Devices.Config,
	)
	if err != nil {
		return nil, fmt.Errorf("error getting opaque device configs: %w", err)
	}

	var kvConfig *configapi.KVCachePoolConfig
	var requests []string
	for _, c := range configs {
		if cfg, ok := c.Config.(*configapi.KVCachePoolConfig); ok {
			kvConfig = cfg
			requests = c.Requests
			break
		}
	}
	if kvConfig == nil {
		return nil, permanentError{fmt.Errorf("no KVCachePoolConfig found in claim")}
	}

	pool, err := s.resolveKVCachePool(ctx, claim, kvConfig)
	if err != nil {
		return nil, err
	}
	if err := validatePoolReady(pool); err != nil {
		return nil, err
	}

	transport := transportForTier(pool.Spec.LatencyTier)
	endpoint := pool.Status.Endpoint

	var preparedDevices PreparedDevices
	for _, result := range claim.Status.Allocation.Devices.Results {
		if result.Driver != DriverName {
			continue
		}

		sliceName := fmt.Sprintf("%s-%s", claim.UID, result.Device)
		preparedDevices = append(preparedDevices, &PreparedKVCacheSlice{
			KVCachePoolName:  pool.Name,
			SliceName:        sliceName,
			KVCacheEndpoint:  endpoint,
			KVCacheTransport: transport,
			CapacityBytes:    kvConfig.CapacityBytes,
			Requests:         requests,
			PoolName:         result.Pool,
			DeviceName:       result.Device,
		})
	}

	if len(preparedDevices) == 0 {
		return nil, fmt.Errorf("no allocation results for driver %s in claim %s", DriverName, ResourceClaimToString(claim))
	}

	for _, slice := range preparedDevices {
		slice.CDIDeviceIDs = []string{s.cdi.GetClaimDevice(string(claim.UID), slice)}
	}

	klog.Infof("Prepared KV cache for pool %q endpoint %q (claim %s)", pool.Name, endpoint, ResourceClaimToString(claim))
	return preparedDevices, nil
}

// resolveKVCachePool loads the KVCachePool chosen for this claim. Match-or-provision
// is owned by the cluster controller (plans/v0.md); the kubelet plugin only reads
// status.endpoint from the bound pool.
func (s *DeviceState) resolveKVCachePool(ctx context.Context, claim *resourceapi.ResourceClaim, cfg *configapi.KVCachePoolConfig) (*configapi.KVCachePool, error) {
	if err := cfg.Normalize(); err != nil {
		return nil, permanentError{fmt.Errorf("normalize KVCachePoolConfig: %w", err)}
	}
	if err := cfg.Validate(); err != nil {
		return nil, permanentError{fmt.Errorf("invalid KVCachePoolConfig: %w", err)}
	}

	poolName, err := cfg.ResolvedPoolName(claim.Annotations)
	if err != nil {
		return nil, err
	}

	pool, err := s.nvclient.ResourceV1beta1().KVCachePools().Get(ctx, poolName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			if cfg.PoolName != "" {
				return nil, permanentError{fmt.Errorf("KVCachePool %q not found: %w", poolName, err)}
			}
			return nil, fmt.Errorf("KVCachePool %q not found (provision may be in progress): %w", poolName, err)
		}
		return nil, fmt.Errorf("get KVCachePool %q: %w", poolName, err)
	}
	return pool, nil
}

func validatePoolReady(pool *configapi.KVCachePool) error {
	if pool.Status.Status != configapi.KVCachePoolStatusReady {
		return fmt.Errorf("KVCachePool %q is not ready (status=%q)", pool.Name, pool.Status.Status)
	}
	if pool.Status.Endpoint == "" {
		return fmt.Errorf("KVCachePool %q has no status.endpoint", pool.Name)
	}
	return nil
}

func transportForTier(tier string) string {
	if tier == "" {
		tier = configapi.KVCacheLatencyTierDRAM
	}
	switch tier {
	case configapi.KVCacheLatencyTierDRAM:
		return defaultKVCacheTransport
	default:
		// v0 only exercises DRAM/LMCache (tcp). Other tiers will get explicit
		// transport values when HBM support lands.
		return defaultKVCacheTransport
	}
}

// GetOpaqueDeviceConfigs returns an ordered list of the configs contained in possibleConfigs for this driver.
//
// Configs can either come from the resource claim itself or from the device
// class associated with the request. Configs coming directly from the resource
// claim take precedence over configs coming from the device class. Moreover,
// configs found later in the list of configs attached to its source take
// precedence over configs found earlier in the list for that source.
//
// All of the configs relevant to the driver from the list of possibleConfigs
// will be returned in order of precedence (from lowest to highest). If no
// configs are found, nil is returned.
func GetOpaqueDeviceConfigs(
	decoder runtime.Decoder,
	driverName string,
	possibleConfigs []resourceapi.DeviceAllocationConfiguration,
) ([]*OpaqueDeviceConfig, error) {
	var classConfigs []resourceapi.DeviceAllocationConfiguration
	var claimConfigs []resourceapi.DeviceAllocationConfiguration
	var candidateConfigs []resourceapi.DeviceAllocationConfiguration
	for _, config := range possibleConfigs {
		switch config.Source {
		case resourceapi.AllocationConfigSourceClass:
			classConfigs = append(classConfigs, config)
		case resourceapi.AllocationConfigSourceClaim:
			claimConfigs = append(claimConfigs, config)
		default:
			return nil, fmt.Errorf("invalid config source: %v", config.Source)
		}
	}
	candidateConfigs = append(candidateConfigs, classConfigs...)
	candidateConfigs = append(candidateConfigs, claimConfigs...)

	var resultConfigs []*OpaqueDeviceConfig
	for _, config := range candidateConfigs {
		if config.Opaque == nil {
			return nil, fmt.Errorf("only opaque parameters are supported by this driver")
		}
		if config.Opaque.Driver != driverName {
			continue
		}

		decodedConfig, err := runtime.Decode(decoder, config.Opaque.Parameters.Raw)
		if err != nil {
			return nil, permanentError{fmt.Errorf("error decoding config parameters: %w", err)}
		}

		resultConfigs = append(resultConfigs, &OpaqueDeviceConfig{
			Requests: config.Requests,
			Config:   decodedConfig,
		})
	}

	return resultConfigs, nil
}
