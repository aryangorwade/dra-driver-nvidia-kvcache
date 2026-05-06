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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	configapi "sigs.k8s.io/dra-driver-nvidia-gpu/api/nvidia.com/resource/v1beta1"
)

type OpaqueDeviceConfig struct {
	Requests []string
	Config   runtime.Object
}

type DeviceState struct {
	sync.Mutex
	cdi                      *CDIHandler
	config                   *Config
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
		cdi:    cdi,
		config: config,
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
	// TODO: implement
	return nil, nil
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
	// Collect all configs in order of reverse precedence.
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

	// Decode all configs that are relevant for the driver.
	var resultConfigs []*OpaqueDeviceConfig
	for _, config := range candidateConfigs {
		// If this is nil, the driver doesn't support some future API extension
		// and needs to be updated.
		if config.Opaque == nil {
			return nil, fmt.Errorf("only opaque parameters are supported by this driver")
		}

		// Configs for different drivers may have been specified because a
		// single request can be satisfied by different drivers. This is not
		// an error -- drivers must skip over other driver's configs in order
		// to support this.
		if config.Opaque.Driver != driverName {
			continue
		}

		decodedConfig, err := runtime.Decode(decoder, config.Opaque.Parameters.Raw)
		if err != nil {
			// Bad opaque config: i) do not retry preparing this resource
			// internally and ii) return notion of permanent error to kubelet,
			// to give it an opportunity to play this error back to the user so
			// that it becomes actionable.
			return nil, permanentError{fmt.Errorf("error decoding config parameters: %w", err)}
		}

		resultConfig := &OpaqueDeviceConfig{
			Requests: config.Requests,
			Config:   decodedConfig,
		}

		resultConfigs = append(resultConfigs, resultConfig)
	}

	return resultConfigs, nil
}
