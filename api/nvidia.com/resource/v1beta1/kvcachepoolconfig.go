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

package v1beta1

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// KVCachePoolConfig holds the set of parameters for configuring a KVCachePool allocation.
type KVCachePoolConfig struct {
	metav1.TypeMeta `json:",inline"`
	
	// PoolID is the unique identifier of the KVCachePool being claimed.
	PoolID string `json:"poolID"`
	// CapacityBytes is the amount of capacity requested from the pool.
	// +optional
	CapacityBytes int64 `json:"capacityBytes,omitempty"`
}

// DefaultKVCachePoolConfig provides the default KVCachePoolConfig.
func DefaultKVCachePoolConfig() *KVCachePoolConfig {
	return &KVCachePoolConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: GroupName + "/" + Version,
			Kind:       KVCachePoolConfigKind, // We added this constant to api.go earlier
		},
	}
}

// Normalize updates a KVCachePoolConfig with implied default values.
func (c *KVCachePoolConfig) Normalize() error {
	return nil
}

// Validate ensures the config has a valid set of values before the kubelet plugin tries to apply it.
// Required to satisfy the v1beta1.Interface.
func (c *KVCachePoolConfig) Validate() error {
	if c.PoolID == "" {
		return fmt.Errorf("poolID cannot be empty")
	}
	if c.CapacityBytes < 1 {
		return fmt.Errorf("capacityBytes must be at least 1")
	}
	return nil
}
