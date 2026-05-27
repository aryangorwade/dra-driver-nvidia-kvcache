# KV Cache DRA Driver — v0 Technical Roadmap

**Goal**: A bare-minimum, compiling, deployable DRA driver that injects a single
data-plane endpoint (e.g. LMCache) into vLLM pods via CDI environment variables.
No GPU-memory fragmentation, no multi-pool scheduling, no topology awareness.

---

## Current State (as of 2026-05-12)

### ✅ Done
| Component | File(s) | Status |
|---|---|---|
| API types: `KVCachePool` CRD | `api/.../kvcachepool.go` | Complete — spec, status, deepcopy |
| API types: `KVCachePoolConfig` opaque config | `api/.../kvcachepoolconfig.go` | Complete — PoolID, CapacityBytes, Normalize/Validate |
| Scheme registration | `api/.../api.go`, `register.go` | Both types registered in scheme + decoders |
| `main.go` entry point | `cmd/kvcache-kubelet-plugin/main.go` | Clean — NVIDIA hooks removed, flags correct |
| `driver.go` plumbing | `cmd/kvcache-kubelet-plugin/driver.go` | Clean — no workqueue, no checkpoint manager |
| `device_state.go` skeleton | `cmd/kvcache-kubelet-plugin/device_state.go` | Prepare/Unprepare gutted, `prepareDevices` implemented |
| `health.go` healthcheck | `cmd/kvcache-kubelet-plugin/health.go` | Unchanged, generic — works as-is |
| `types.go` helpers | `cmd/kvcache-kubelet-plugin/types.go` | Has `ResourceClaimToString` — still has stale IMEX consts |

### ❌ Broken / Not Started
| Component | File(s) | Problem |
|---|---|---|
| `cdi.go` | `cmd/kvcache-kubelet-plugin/cdi.go` | **CRITICAL**: Still imports `go-nvml`, `go-nvlib`, `nvcdi`. Uses NVIDIA hardware types in `CDIHandler` struct, `NewCDIHandler`, `CreateClaimSpecFile`, `CreateStandardDeviceSpecFile`. Will not compile. |
| `cdioptions.go` | `cmd/kvcache-kubelet-plugin/cdioptions.go` | Imports `go-nvml`, `go-nvlib` for `WithNvml`, `WithDeviceLib` options. Must be gutted. |
| `root.go` | `cmd/kvcache-kubelet-plugin/root.go` | Searches for `libnvidia-ml.so.1` and `nvidia-smi`. Entirely hardware-specific — delete or empty. |
| `indexers.go` | `cmd/kvcache-kubelet-plugin/indexers.go` | Generic UID indexer — fine but unused currently. |
| `types.go` | `cmd/kvcache-kubelet-plugin/types.go` | Still has `ComputeDomainChannelType`/`ComputeDomainDaemonType` consts — stale. |
| ResourceSlice publishing | Not implemented | Plugin does not advertise any "kvcache" devices to the scheduler. Pods cannot be scheduled without this. |
| Deployment manifests | Not created | No DaemonSet, RBAC, or DeviceClass YAML. |
| End-to-end test | Not created | No ResourceClaim YAML or test Pod. |

---

## Roadmap

### Phase 1: Make it Compile

> **Objective**: `go build ./cmd/kvcache-kubelet-plugin/...` succeeds.

#### 1.1 Rewrite `cdi.go`

This is the single biggest blocker. The entire file must be rewritten to
remove NVIDIA hardware dependencies and inject environment variables instead
of device nodes.

**Delete**:
- All `github.com/NVIDIA/*` imports
- `CreateStandardDeviceSpecFile` (no base GPU spec needed)
- `GetStandardDevice`, `GetClaimDevice` (reference deleted types)
- NVML init/shutdown in constructor
- `nvcdiDevice`, `nvcdiClaim`, `nvml`, `nvdevice` fields from `CDIHandler`
- `driverRoot`, `devRoot`, `targetDriverRoot`, `nvidiaCDIHookPath` fields
- The `transformroot` import and all transform logic

**Keep**:
- `cache *cdiapi.Cache` field
- `cdiRoot`, `vendor`, `deviceClass`, `claimClass` fields
- `DeleteClaimSpecFileIfExists` (already correct)
- CDI constants (`cdiVendor`, `cdiClaimClass`, etc.)

**Rewrite `NewCDIHandler`**:
```go
func NewCDIHandler(opts ...cdiOption) (*CDIHandler, error) {
    h := &CDIHandler{}
    for _, opt := range opts { opt(h) }
    if h.cdiRoot == "" { h.cdiRoot = defaultCDIRoot }
    if h.vendor == "" { h.vendor = cdiVendor }
    if h.claimClass == "" { h.claimClass = cdiClaimClass }
    cache, err := cdiapi.NewCache(cdiapi.WithSpecDirs(h.cdiRoot))
    if err != nil { return nil, err }
    h.cache = cache
    return h, nil
}
```

**Rewrite `CreateClaimSpecFile`** to accept `(claimUID string, devices PreparedDevices)`:
- Build a single CDI device spec per prepared device
- Set `ContainerEdits.Env` to inject `KVCACHE_POOL_ID=<poolID>` (v0)
- Write to cache

#### 1.2 Rewrite `cdioptions.go`

Remove all options that reference NVIDIA types:
- `WithDriverRoot`, `WithDevRoot`, `WithTargetDriverRoot`, `WithNVIDIACDIHookPath`
- `WithNvml`, `WithDeviceLib`

Keep:
- `WithCDIRoot`, `WithVendor`

#### 1.3 Delete or empty `root.go`

This file searches for `libnvidia-ml.so.1` and `nvidia-smi`. It is entirely
hardware-specific and unused by the KV cache plugin. **Delete the file entirely**
or replace with an empty placeholder.

#### 1.4 Clean up `types.go`

Remove the stale `ComputeDomainChannelType` / `ComputeDomainDaemonType` /
`UnknownDeviceType` constants. Keep `ResourceClaimToString`.

#### 1.5 Clean up unused imports across all files

After the above changes, run `go build` and fix any remaining import errors.
Key ones to watch:
- `device_state.go`: `cdiapi` import is unused now (was used by deleted `DeviceConfigState`)
- `driver.go`: `ErrorRetryMaxTimeout` const is unused (workqueue gone) — remove or keep for future use
- `main.go`: `DriverPluginCheckpointFileBasename` const — remove

---

### Phase 2: ResourceSlice Publishing

> **Objective**: The scheduler can see "kvcache" devices and allocate claims.

Without publishing a ResourceSlice, the Kubernetes scheduler has no idea your
driver exists. Pods with ResourceClaims referencing `kvcache.nvidia.com` will
sit in `Pending` forever.

#### 2.1 Add ResourceSlice publishing to `driver.go`

After `kubeletplugin.Start(...)`, call `helper.PublishResources(...)` with a
single pool containing one virtual device:

```go
resources := resourceslice.DriverResources{
    Pools: map[string]resourceslice.Pool{
        config.flags.nodeName: {
            Slices: []resourceslice.Slice{{
                Devices: []resourceapi.Device{{
                    Name: "kvcache-slot",
                    // No attributes needed for v0
                }},
            }},
        },
    },
}
helper.PublishResources(ctx, resources)
```

This tells the scheduler: "this node has a kvcache slot available."

#### 2.2 Decide: static device vs. dynamic

For v0, a single static "kvcache-slot" device per node is sufficient. The
scheduler will allocate at most one claim per slot. In v1, you can advertise
multiple slots or use attributes to express capacity.

---

### Phase 3: Endpoint Injection via CDI

> **Objective**: When a Pod starts, it gets `KVCACHE_POOL_ID` and
> `KVCACHE_ENDPOINT` injected as environment variables.

#### 3.1 Wire `prepareDevices` → `CreateClaimSpecFile`

`prepareDevices` already extracts the `PoolID` from the claim config. Now it
needs to also resolve the data-plane endpoint. For v0:

**Option A (hardcoded)**: Hardcode a well-known endpoint in
`prepareDevices`, e.g. `"lmcache-server.default.svc.cluster.local:8080"`.

**Option B (CRD lookup)**: Query the `KVCachePool` CRD by `PoolID` to get
`spec.dataPlane.endpoint`. This requires adding a Kubernetes client to
`DeviceState`.

**Recommendation**: Use Option B. It is only ~10 lines more code and makes
the system actually functional. The CRD already has `DataPlane.Endpoint`.

#### 3.2 Implement the CDI spec write

`CreateClaimSpecFile` should produce a JSON file at `/var/run/cdi/` that
looks like:

```json
{
  "cdiVersion": "0.6.0",
  "kind": "k8s.kvcache.nvidia.com/claim",
  "devices": [{
    "name": "<claimUID>",
    "containerEdits": {
      "env": [
        "KVCACHE_POOL_ID=my-pool",
        "KVCACHE_ENDPOINT=lmcache-server.default.svc.cluster.local:8080"
      ]
    }
  }]
}
```

The container runtime reads this and injects the env vars into the Pod.

---

### Phase 4: Deployment Manifests

> **Objective**: `kubectl apply -f deploy/` brings the plugin up on a cluster.

#### 4.1 Create `deploy/kvcache-kubelet-plugin/`

Files needed:
- `daemonset.yaml` — runs the plugin on every node (or selected nodes)
- `rbac.yaml` — ServiceAccount + ClusterRole + ClusterRoleBinding
- `deviceclass.yaml` — `DeviceClass` resource that references `kvcache.nvidia.com`

#### 4.2 Create a sample `KVCachePool` CR

```yaml
apiVersion: resource.nvidia.com/v1beta1
kind: KVCachePool
metadata:
  name: llama3-8b-pool
spec:
  capacityBytes: 8589934592  # 8 GiB
  engine: vllm
  modelFamily: llama3-8b
  dtype: fp16
  blockSizeTokens: 16
  latencyTier: DRAM
  dataPlane:
    provider: lmcache
    endpoint: "lmcache-server.default.svc.cluster.local:8080"
```

#### 4.3 Create a sample ResourceClaim + test Pod

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: test-kvcache-claim
spec:
  devices:
    requests:
    - name: kvcache
      deviceClassName: kvcache
    config:
    - opaque:
        driver: kvcache.nvidia.com
        parameters:
          apiVersion: resource.nvidia.com/v1beta1
          kind: KVCachePoolConfig
          poolID: llama3-8b-pool
          capacityBytes: 1073741824  # 1 GiB
---
apiVersion: v1
kind: Pod
metadata:
  name: test-kvcache-consumer
spec:
  resourceClaims:
  - name: kvcache-claim
    resourceClaimName: test-kvcache-claim
  containers:
  - name: debug
    image: busybox
    command: ["sh", "-c", "env | grep KVCACHE && sleep 3600"]
    resources:
      claims:
      - name: kvcache-claim
```

---

### Phase 5: End-to-End Validation

> **Objective**: Deploy on a real cluster, see env vars inside a pod.

#### 5.1 Build the binary
```bash
go build -o kvcache-kubelet-plugin ./cmd/kvcache-kubelet-plugin/
```

#### 5.2 Deploy to a kind/minikube cluster
```bash
kubectl apply -f deploy/kvcache-kubelet-plugin/
kubectl apply -f examples/kvcachepool.yaml
kubectl apply -f examples/test-pod.yaml
```

#### 5.3 Verify
```bash
kubectl exec test-kvcache-consumer -- env | grep KVCACHE
# Expected:
# KVCACHE_POOL_ID=llama3-8b-pool
# KVCACHE_ENDPOINT=lmcache-server.default.svc.cluster.local:8080
```

---

## Execution Order (Priority)

| Step | Phase | Description | Blocks |
|------|-------|-------------|--------|
| 1 | 1.1 | Rewrite `cdi.go` | Everything — nothing compiles without this |
| 2 | 1.2 | Rewrite `cdioptions.go` | Compile |
| 3 | 1.3 | Delete `root.go` | Compile |
| 4 | 1.4 | Clean `types.go` | Compile |
| 5 | 1.5 | Fix remaining imports, `go build` | Phase 2+ |
| 6 | 2.1 | ResourceSlice publishing | Scheduling |
| 7 | 3.1 | Endpoint resolution in `prepareDevices` | Injection |
| 8 | 3.2 | CDI spec write with env vars | Injection |
| 9 | 4.1-4.3 | Deployment + example manifests | Testing |
| 10 | 5.1-5.3 | Build, deploy, verify | Done |

---

## v1 Roadmap

> **Prerequisite**: v0 is deployed and verified end-to-end.

### v1.1 — KVCachePool Controller
- New binary: `cmd/kvcache-controller/`
- Watches `KVCachePool` CRDs, reconciles `.status` (allocatedBytes, node list, readiness)
- Tracks active `ResourceClaim` allocations against pool capacity
- Sets `status.status = Ready` once data-plane endpoint is reachable

### v1.2 — Capacity-Aware Scheduling
- Publish ResourceSlice attributes: `pool-id`, `available-bytes`, `engine`, `model-family`
- Scheduler uses CEL selectors to match claims to pools with sufficient capacity
- `prepareDevices` decrements available capacity; `Unprepare` increments it back
- **Introduce checkpointing here**: once the driver owns node-local byte accounting
  (e.g. how many HBM bytes are currently allocated per GPU), that state does not exist
  anywhere in Kubernetes. A checkpoint file ensures the driver does not double-allocate
  on crash/restart. Not needed in v0 because all state is derivable from the
  `KVCachePool` CRD and active `ResourceClaim` objects in the Kubernetes API.

### v1.3 — Multi-Pool / Multi-Slot per Node
- Advertise N device slots per node (one per active pool binding)
- Support multiple `KVCachePoolConfig` entries in a single claim
- Track per-claim byte allocations in controller status

### v1.4 — Disaggregated Serving Support (Dynamo)
- Add `Role` field to `KVCachePoolConfig` (`prefill | decode | both`)
- Dynamo and similar data-planes use this to know which pods write KV blocks
  (prefill workers) vs read them (decode workers), enabling P/D split without
  inspecting pod labels out-of-band
- Both prefill and decode pods claim the same `KVCachePool`; Role disambiguates
  their function to the routing layer
- No conflict with current API: v0/v1 pods that omit Role default to `both`
- This is purely additive — does not change any existing scheduling behavior

**Example ResourceClaims:**
```yaml
# 1. The Prefill Pod's Claim
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: prefill-claim
spec:
  devices:
    config:
    - opaque:
        driver: kvcache.nvidia.com
        parameters:
          apiVersion: resource.nvidia.com/v1beta1
          kind: KVCachePoolConfig
          poolID: my-giant-pool
          role: prefill # <--- Driver injects KVCACHE_ROLE=prefill
---
# 2. The Decode Pod's Claim
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: decode-claim
spec:
  devices:
    config:
    - opaque:
        driver: kvcache.nvidia.com
        parameters:
          apiVersion: resource.nvidia.com/v1beta1
          kind: KVCachePoolConfig
          poolID: my-giant-pool
          role: decode # <--- Driver injects KVCACHE_ROLE=decode
```

### v1.5 — Tiered KV Offloading Support (llm-d / tiered data-planes)
- Rename `latencyTier` → `primaryTier` in `KVCachePoolSpec` to reflect that
  tiered data-planes (llm-d) move blocks dynamically between HBM → DRAM → Disk
- `primaryTier` = where hot blocks reside; data-plane may spill cold blocks to
  slower tiers transparently
- Clarify `capacityBytes` semantics: addressable capacity of primary tier.,
- Inject additional env var `KVCACHE_PRIMARY_TIER=HBM` so the vLLM connector
  can tune its prefetch aggressiveness accordingly

**Concept: Local Execution Cache vs. Shared Pool:**
It is critical to distinguish vLLM's mandatory local HBM from the `KVCachePool`.
- **Local HBM:** vLLM *always* requires local HBM to compute/execute the model. This is local to the pod and not managed by our driver.
- **Shared Pool (`KVCachePool`):** This is where blocks go *after* computation so other pods can use them. 
  - If `primaryTier: DRAM`, the connector copies computed blocks from local HBM over the network to the shared DRAM pool (e.g., LMCache).
  - If `primaryTier: HBM`, the shared pool is fragmented HBM space across the node, and blocks are shared via NVLink.
Our abstraction dictates the storage medium of the *shared* pool, never the local execution cache.

**Design Philosophy: Abstraction Boundaries & Secondary Tiers**
- **No Secondary Tier API:** The `KVCachePool` API *only* models the `primaryTier` (e.g., HBM). We **do not** model secondary/tertiary fallback tiers (like DRAM or Disk) in Kubernetes. The K8s scheduler only uses the primary tier for admission control to guarantee fast performance. Defining secondary tiers in K8s creates dead API surface, breaks node-agnosticism, and intrudes on what the data-plane (Dynamo/llm-d) already manages optimally on its own.
- **The Role of the Endpoint (`DYNAMO_MASTER_ADDR`):** When the driver injects the endpoint (translating `spec.dataPlane.endpoint` into `DYNAMO_MASTER_ADDR`), it is *not* pointing to a hard drive to "store data here." It points the pod to the Dynamo Control Plane / Master. The pod uses this to join the peer-to-peer network. The Master simply acts as a directory tracking which worker pods hold which blocks in their local HBM.
- **Why this Abstraction is Necessary:** Without this DRA driver, users would have to manually hardcode `DYNAMO_MASTER_ADDR` into every Pod YAML. This would be disastrous because the Kubernetes Scheduler would have ZERO awareness of cache capacity, blindly scheduling pods until the cache overfills and crashes. Our driver enables users to use standard `ResourceClaims` for safe admission control, while the driver invisibly handles the fragile endpoint injection behind the scenes.

### v1.6 — Transport Integration
- Wire `spec.dataPlane.transport.type` into CDI env vars (`KVCACHE_TRANSPORT=rdma`)
- Support NIXL/UCX transport endpoints alongside TCP
- Inject transport-specific config (e.g. RDMA device name, GID index)

### v1.5 — Admission Webhook
- Validate `KVCachePoolConfig` at admission time (pool exists, capacity available, engine match)
- Reject claims that reference non-existent pools or exceed capacity

### v1.6 — Topology-Aware Placement
- Annotate nodes with GPU/NIC topology info
- Scheduler prefers co-located nodes for KV transfer locality
- Integrate with `dra-driver-nvidia-gpu` for GPU↔KVCache affinity

---

## Repo Migration Plan

> **When**: After v0 is verified on a real cluster. Before v1 development begins.

### Why Migrate
The current code lives inside the `dra-driver-nvidia-gpu` fork. This was
useful for bootstrapping (reusing codegen, go.mod, Makefile) but creates
long-term coupling to NVIDIA GPU code we don't need.

### Target Structure
Clone [`kubernetes-sigs/dra-example-driver`](https://github.com/kubernetes-sigs/dra-example-driver)
into a new repo: `github.com/<org>/dra-driver-kvcache`.

```
dra-driver-kvcache/
├── api/kvcache.io/v1beta1/       # Copy from api/nvidia.com/resource/v1beta1/kvcache*.go
├── cmd/kvcache-kubelet-plugin/   # Copy entire directory
├── cmd/kvcache-controller/       # New in v1
├── deploy/                       # DaemonSet, RBAC, DeviceClass
├── examples/                     # Sample CRs and test pods
├── go.mod                        # Fresh module, no NVIDIA deps
└── Makefile                      # controller-gen, deepcopy, docker build
```

### Migration Steps

1. **Create new repo** from `dra-example-driver` template
2. **Copy API types**: `kvcachepool.go`, `kvcachepoolconfig.go`, `api.go`, `register.go`, `zz_generated.deepcopy.go`
3. **Copy plugin code**: entire `cmd/kvcache-kubelet-plugin/` directory
4. **Update import paths**: `s/sigs.k8s.io\/dra-driver-nvidia-gpu/github.com\/<org>\/dra-driver-kvcache/g`
5. **Strip `go.mod`**: Remove all `github.com/NVIDIA/*` dependencies
6. **Run `go mod tidy`** — verify no NVIDIA transitive deps remain
7. **`go build ./...`** — confirm clean compilation
8. **Copy deploy manifests** and update image references
9. **Tag `v0.1.0`** — first standalone release
