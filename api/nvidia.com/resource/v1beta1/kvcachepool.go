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
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:openapi-gen=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status

// KVCachePool represents a logical pool of KV cache capacity that can be shared
// across inference workloads. Workloads with compatible KV formats may share KV
// blocks directly. The KVCachePool controller
// reconciles this API object and coordinates capacity allocation. Actual KV
// storage and transfer are provided either by the project's default compatible
// data plane or by an explicitly configured external data-plane provider such as
// LMCache or Dynamo. Workloads claim slices of pool capacity via standard K8s
// ResourceClaims.
//
// NOTE: Future plans allow workloads with incompatible KV formats may allocate
// separate, isolated slices from the same pool. This would involve creating 
// a custom dataplane that does this.
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
type KVCachePoolSpec struct {
	// CapacityBytes is the total capacity of the KV cache pool in bytes.
	// +kubebuilder:validation:Minimum=1
	CapacityBytes int64 `json:"capacityBytes"`

	// LatencyTier describes the memory/storage tier backing the pool. TODO: HBM's direct GPU memory.
	// +kubebuilder:validation:Enum=HBM;DRAM;Disk
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

	// PodSelector selects inference pods that may use this KV cache pool.
	// +optional
	PodSelector map[string]string `json:"podSelector,omitempty"`

	// NodeSelector selects nodes where this pool is available.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// DataPlane specifies an optional external data-plane provider and transport
	// configuration for this pool. If omitted, the KVCachePool controller uses a
	// default compatible dataplane for coordinating access to the shared pool. If
	// specified, the controller integrates with this provider instead to coordinate
	// access.
	// +optional
	DataPlane *KVCacheDataPlaneSpec `json:"dataPlane,omitempty"`
}

// KVCacheDataPlaneSpec describes the data-plane provider backing the pool.
type KVCacheDataPlaneSpec struct {
	// Provider is the higher-level KV cache implementation that the controller
	// integrates with.
	// Examples: "lmcache", "dynamo".
	// +kubebuilder:validation:Enum=lmcache;dynamo
	Provider string `json:"provider"`

	// Endpoint is the network address of the provider.
	// Injected into claiming pods as an environment variable at bind time.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Transport describes the lower-level data movement mechanism used by the provider.
	// +optional
	Transport *KVCacheTransportSpec `json:"transport,omitempty"`
}

// KVCacheTransportSpec describes how KV bytes move between processes/nodes.
type KVCacheTransportSpec struct {
	// Type is the transport mechanism.
	// Examples: "tcp", "rdma", "ucx", "nixl".
	// +kubebuilder:validation:Enum=tcp;rdma;ucx;nixl
	Type string `json:"type"`

	// Endpoint is the transport-specific endpoint, if different from the provider endpoint.
	// Most v0 implementations should omit this and use the provider endpoint.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
}

// KVCachePoolStatus provides the status for a KVCachePool.
type KVCachePoolStatus struct {
	// +kubebuilder:validation:Enum=Ready;NotReady
	// +kubebuilder:default=NotReady
	Status string `json:"status"`

	// AllocatedBytes tracks how many bytes of pool capacity have been
	// allocated to active claims.
	AllocatedBytes int64 `json:"allocatedBytes,omitempty"`

	// +listType=map
	// +listMapKey=name
	Nodes []*KVCachePoolNode `json:"nodes,omitempty"`
}

// KVCachePoolNode provides information about each node participating in a KVCachePool.
type KVCachePoolNode struct {
	Name      string `json:"name"`
	IPAddress string `json:"ipAddress,omitempty"`
	// +optional
	// +kubebuilder:validation:Enum=Ready;NotReady
	// +kubebuilder:default=NotReady
	Status string `json:"status,omitempty"`
}
