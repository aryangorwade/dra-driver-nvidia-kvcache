/*
Copyright The Kubernetes Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"fmt"

	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdiparser "tags.cncf.io/container-device-interface/pkg/parser"
	cdispec "tags.cncf.io/container-device-interface/specs-go"
)

const (
	cdiVendor = "k8s." + DriverName

	cdiClaimClass = "claim"
	cdiClaimKind  = cdiVendor + "/" + cdiClaimClass

	defaultCDIRoot          = "/var/run/cdi"
	defaultKVCacheTransport = "tcp"
)

// cdiEnvForSlice returns the env vars injected into the workload container.
func cdiEnvForSlice(slice *PreparedKVCacheSlice) []string {
	transport := slice.KVCacheTransport
	if transport == "" {
		transport = defaultKVCacheTransport
	}
	return []string{
		fmt.Sprintf("KVCACHE_POOL_ID=%s", slice.KVCachePoolName),
		fmt.Sprintf("KVCACHE_SLICE_NAME=%s", slice.SliceName),
		fmt.Sprintf("KVCACHE_ENDPOINT=%s", slice.KVCacheEndpoint),
		fmt.Sprintf("KVCACHE_TRANSPORT=%s", transport),
	}
}

// CDIHandler writes and removes per-claim CDI spec files that inject
// KV cache pool configuration as environment variables into containers.
// No GPU hardware interaction occurs here.
type CDIHandler struct {
	cache      *cdiapi.Cache
	cdiRoot    string
	vendor     string
	claimClass string
}

func NewCDIHandler(opts ...cdiOption) (*CDIHandler, error) {
	h := &CDIHandler{}
	for _, opt := range opts {
		opt(h)
	}
	if h.cdiRoot == "" {
		h.cdiRoot = defaultCDIRoot
	}
	if h.vendor == "" {
		h.vendor = cdiVendor
	}
	if h.claimClass == "" {
		h.claimClass = cdiClaimClass
	}
	cache, err := cdiapi.NewCache(cdiapi.WithSpecDirs(h.cdiRoot))
	if err != nil {
		return nil, fmt.Errorf("unable to create CDI cache: %w", err)
	}
	h.cache = cache
	return h, nil
}

// CreateClaimSpecFile writes a CDI spec file for the given claim that injects
// pool endpoint information as environment variables into the container.
//
// Each PreparedKVCacheSlice produces one CDI device entry named
// "<claimUID>-<sliceName>", carrying the env vars the vLLM / TRT-LLM /
// SGLang KV connector needs to find the shared pool.
func (cdi *CDIHandler) CreateClaimSpecFile(claimUID string, devices PreparedDevices) error {
	var deviceSpecs []cdispec.Device
	for _, slice := range devices {
		if slice == nil {
			continue
		}
		deviceSpecs = append(deviceSpecs, cdispec.Device{
			Name: fmt.Sprintf("%s-%s", claimUID, slice.SliceName),
			ContainerEdits: cdispec.ContainerEdits{
				Env: cdiEnvForSlice(slice),
			},
		})
	}

	if len(deviceSpecs) == 0 {
		return nil
	}

	raw := &cdispec.Spec{
		Version: "0.6.0",
		Kind:    cdiClaimKind,
		Devices: deviceSpecs,
	}

	minVersion, err := cdispec.MinimumRequiredVersion(raw)
	if err != nil {
		return fmt.Errorf("failed to get minimum CDI spec version: %w", err)
	}
	raw.Version = minVersion

	specName := cdiapi.GenerateTransientSpecName(cdi.vendor, cdi.claimClass, claimUID)
	return cdi.cache.WriteSpec(raw, specName)
}

// DeleteClaimSpecFileIfExists removes the CDI spec file for this claim.
func (cdi *CDIHandler) DeleteClaimSpecFileIfExists(claimUID string) error {
	specName := cdiapi.GenerateTransientSpecName(cdi.vendor, cdi.claimClass, claimUID)
	return cdi.cache.RemoveSpec(specName)
}

// GetClaimDevice returns the qualified CDI device name for a given claim +
// slice. Returns empty string if the slice is nil (no CDI device to inject).
func (cdi *CDIHandler) GetClaimDevice(claimUID string, slice *PreparedKVCacheSlice) string {
	if slice == nil {
		return ""
	}
	return cdiparser.QualifiedName(cdi.vendor, cdi.claimClass,
		fmt.Sprintf("%s-%s", claimUID, slice.SliceName))
}
