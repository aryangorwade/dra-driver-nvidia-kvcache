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
	"fmt"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
)

const (
	KVCacheSliceType = 	"kvcache-slice"
	UnknownDeviceType = "unknown"
)

type PreparedDevices []*PreparedKVCacheSlice

type PreparedKVCacheSlice struct {
	// KVCachePoolName is the KVCachePool.metadata.name
	KVCachePoolName string `json:"kvCachePoolName"`

	// SliceName uniquely identifies this claim's slice within the pool.
	SliceName string `json:"sliceName"`

	// CapacityBytes is the amount of KV cache capacity allocated to this slice.
	CapacityBytes int64 `json:"capacityBytes,omitempty"`

	// These are the fields for device, the kubelet-facing prepared resource.
	// Storing these instead of a kubeletplugin.Device results in cleaner checkpointing.
	Requests     []string `json:"requests,omitempty"`
	// PoolName is the DRA pool name from the allocation result.
	PoolName string `json:"poolName"`
	DeviceName   string   `json:"deviceName"`
	CDIDeviceIDs []string `json:"cdiDeviceIDs,omitempty"`
}

func (d PreparedDevices) GetDevices() []kubeletplugin.Device {
	devices := make([]kubeletplugin.Device, 0, len(d))
	for _, slice := range d {
		if slice == nil {
			continue
		}
		devices = append(devices, kubeletplugin.Device{
			Requests:     slice.Requests,
			PoolName:     slice.PoolName,
			DeviceName:   slice.DeviceName,
			CDIDeviceIDs: slice.CDIDeviceIDs,
		})
	}
	return devices
}

// DRAKVCachePoolName constructs a DRA-compliant pool name from a namespace and resource name.
// Returns pool name as: <namespace>.<name>
func DRAKVCachePoolName(namespace, name string) string {
	return namespace + "." + name
}

func ResourceClaimToString(rc *resourcev1.ResourceClaim) string {
	return fmt.Sprintf("%s/%s:%s", rc.Namespace, rc.Name, rc.UID)
}
