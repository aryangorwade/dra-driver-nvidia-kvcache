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

const (
	KVCacheEngineVLLM   = "vllm"
	KVCacheEngineTRTLLM = "trtllm"
	KVCacheEngineSGLang = "sglang"
)

// KVCachePoolConfig holds the ResourceClaim parameters used to select or create
// a compatible KVCachePool. PoolName is an optional exact-pool override; when it
// is empty, the controller matches/provisions a pool from the compatibility
// fields below.
type KVCachePoolConfig struct {
	metav1.TypeMeta `json:",inline"`

	// PoolName is an optional exact KVCachePool name. If unset, the controller
	// auto-discovers or provisions a compatible pool.
	// +optional
	PoolName string `json:"poolName,omitempty"`

	// CapacityBytes is the amount of capacity requested from the pool.
	// +kubebuilder:validation:Minimum=1
	CapacityBytes int64 `json:"capacityBytes"`

	// LatencyTier describes the memory/storage tier requested for the pool.
	// v0 supports DRAM only.
	// +kubebuilder:validation:Enum=DRAM;HBM
	// +kubebuilder:default=DRAM
	LatencyTier string `json:"latencyTier,omitempty"`

	// Engine identifies the inference engine family whose KV format is needed.
	// +kubebuilder:validation:Enum=vllm;trtllm;sglang
	Engine string `json:"engine"`

	// ModelFamily identifies the model family whose KV blocks are compatible.
	ModelFamily string `json:"modelFamily"`

	// Dtype is the data type of KV blocks requested (for example fp16, bf16, fp8).
	Dtype string `json:"dtype"`

	// BlockSizeTokens is the number of tokens per KV block.
	// +kubebuilder:validation:Minimum=1
	BlockSizeTokens int32 `json:"blockSizeTokens"`
}

// DefaultKVCachePoolConfig provides the default KVCachePoolConfig.
func DefaultKVCachePoolConfig() *KVCachePoolConfig {
	return &KVCachePoolConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: GroupName + "/" + Version,
			Kind:       KVCachePoolConfigKind,
		},
	}
}

// Normalize updates a KVCachePoolConfig with implied default values.
func (c *KVCachePoolConfig) Normalize() error {
	if c.LatencyTier == "" {
		c.LatencyTier = KVCacheLatencyTierDRAM
	}
	return nil
}

// Validate ensures the config has a valid set of values before the kubelet plugin tries to apply it.
// Required to satisfy the v1beta1.Interface.
func (c *KVCachePoolConfig) Validate() error {
	if c.CapacityBytes < 1 {
		return fmt.Errorf("capacityBytes must be at least 1")
	}
	switch c.LatencyTier {
	case KVCacheLatencyTierDRAM, KVCacheLatencyTierHBM:
	default:
		return fmt.Errorf("latencyTier must be one of %q or %q", KVCacheLatencyTierDRAM, KVCacheLatencyTierHBM)
	}
	switch c.Engine {
	case KVCacheEngineVLLM, KVCacheEngineTRTLLM, KVCacheEngineSGLang:
	default:
		return fmt.Errorf("engine must be one of %q, %q, or %q", KVCacheEngineVLLM, KVCacheEngineTRTLLM, KVCacheEngineSGLang)
	}
	if c.ModelFamily == "" {
		return fmt.Errorf("modelFamily cannot be empty")
	}
	if c.Dtype == "" {
		return fmt.Errorf("dtype cannot be empty")
	}
	if c.BlockSizeTokens < 1 {
		return fmt.Errorf("blockSizeTokens must be at least 1")
	}
	return nil
}
