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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	KVCachePoolStatusNone     = ""
	KVCachePoolStatusReady    = "Ready"
	KVCachePoolStatusNotReady = "NotReady"

	// LatencyTier constants for KVCachePool.
	KVCacheLatencyTierHBM  = "HBM"
	KVCacheLatencyTierDRAM = "DRAM"
	KVCacheLatencyTierDisk = "Disk"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:openapi-gen=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status

// KVCachePool represents a logical pool of KV cache capacity that can be shared
// across inference pods with compatible configurations (engine, model family,
// dtype, block size). The KVCachePool controller reconciles this object:
// it either discovers an existing pool that matches a ResourceClaim's parameters
// or provisions a new one. Once ready, the controller writes the authoritative
// data-plane address to Status.Endpoint; the kubelet plugin reads that field at
// claim-prepare time and injects it into the pod as KVCACHE_ENDPOINT.
//
// v0 data plane: LMCache (DRAM, TCP). The controller provisions and manages a
// LMCache Service; no user-supplied data-plane config is accepted.
//
// v1.1+ (HBM): provisioning registers per-node GPU handles with a NIXL index
// service rather than creating an LMCache Service. A separate KVCacheHBMSpec
// field (not yet defined) will carry the index-service endpoint and NIXL
// transport parameters, and will be designed alongside v1.1.
type KVCachePool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec KVCachePoolSpec `json:"spec,omitempty"`
	// Global KVCachePool status. Can be used to guide debugging efforts.
	// Workload however should not rely on inspecting this field at any point
	// during its lifecycle.
	Status KVCachePoolStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// KVCachePoolList provides a list of KVCachePools.
type KVCachePoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []KVCachePool `json:"items"`
}

// KVCachePoolSpec provides the spec for a KVCachePool.
//
// v0 match keys: Engine + ModelFamily + Dtype + BlockSizeTokens + LatencyTier.
// Two ResourceClaims with identical values for all five fields will be matched
// to the same pool by the controller.
type KVCachePoolSpec struct {
	// CapacityBytes is the total capacity of the KV cache pool in bytes.
	// +kubebuilder:validation:Minimum=1
	CapacityBytes int64 `json:"capacityBytes"`

	// LatencyTier describes the memory/storage tier backing the pool.
	// v0 supports DRAM only (LMCache). HBM support is planned for v1.1 via
	// NIXL/RDMA peer-to-peer GPU transfers; a separate KVCacheHBMSpec field
	// will be introduced at that point.
	// +kubebuilder:validation:Enum=HBM;DRAM
	// +kubebuilder:default=DRAM
	LatencyTier string `json:"latencyTier,omitempty"`

	// Engine identifies the inference engine family whose KV format this
	// pool is compatible with (e.g. "vllm", "trtllm", "sglang").
	// +kubebuilder:validation:Enum=vllm;trtllm;sglang
	Engine string `json:"engine"`

	// ModelFamily identifies the model family this pool is compatible with
	// (e.g. "llama3-8b", "llama3-70b").
	ModelFamily string `json:"modelFamily"`

	// Dtype is the data type of KV blocks in this pool (e.g. "fp16", "fp8", "bf16").
	Dtype string `json:"dtype"`

	// BlockSizeTokens is the number of tokens per KV block (e.g. 16 for vLLM default).
	// +kubebuilder:validation:Minimum=1
	BlockSizeTokens int32 `json:"blockSizeTokens"`
}

// KVCachePoolStatus provides the status for a KVCachePool.
type KVCachePoolStatus struct {
	// +kubebuilder:validation:Enum=Ready;NotReady
	// +kubebuilder:default=NotReady
	Status string `json:"status"`

	// Endpoint is the authoritative network address of the data-plane service
	// backing this pool, written by the controller after the pool is provisioned
	// or discovered. The kubelet plugin reads this field at claim-prepare time
	// and injects it into pods as KVCACHE_ENDPOINT.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// AllocatedBytes tracks how many bytes of pool capacity have been
	// allocated to active claims.
	AllocatedBytes int64 `json:"allocatedBytes,omitempty"`
}
