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

// cdiOption is a functional option for constructing a CDIHandler.
type cdiOption func(*CDIHandler)

// WithCDIRoot sets the directory where CDI spec files are written.
func WithCDIRoot(cdiRoot string) cdiOption {
	return func(c *CDIHandler) {
		c.cdiRoot = cdiRoot
	}
}

// WithVendor overrides the CDI vendor string (default: "k8s.<DriverName>").
func WithVendor(vendor string) cdiOption {
	return func(c *CDIHandler) {
		c.vendor = vendor
	}
}

// WithClaimClass overrides the CDI class used for per-claim spec files
// (default: "claim").
func WithClaimClass(class string) cdiOption {
	return func(c *CDIHandler) {
		c.claimClass = class
	}
}
