# KV Cache DRA Driver — Project Status

## What this project is

`dra-driver-nvidia-kvcache` is a Kubernetes Dynamic Resource Allocation (DRA)
driver for sharing LLM KV caches across compatible inference pods.

The v0 goal is:

- A workload requests KV-cache capacity with a Kubernetes `ResourceClaim`.
- The request describes its required KV-cache format: engine, model family,
  dtype, block size, and latency tier.
- Compatible workloads bind to the same cluster-scoped `KVCachePool`.
- The pool is backed by an LMCache DRAM service.
- The kubelet plugin injects the selected pool endpoint into the container
  through CDI environment variables.
- The inference engine uses that endpoint to read and write shared KV blocks.

DRA provides Kubernetes scheduling and admission lifecycle integration. The
actual KV-cache bytes live in the LMCache data plane, not in the DRA driver.

The authoritative v0 scope is [`../plans/v0.md`](../plans/v0.md).

## v0 architecture

```text
ResourceClaim
  └─ KVCachePoolConfig
       ├─ engine / model / dtype / block size / DRAM
       └─ requested capacity

kvcache controller
  └─ match existing compatible KVCachePool
     or provision a new one
       └─ LMCache Service
       └─ KVCachePool.status.endpoint
       └─ bind ResourceClaim → KVCachePool

kubelet plugin
  └─ receives allocated claim on a node
  └─ reads bound KVCachePool
  └─ verifies Ready + status.endpoint
  └─ writes CDI spec that injects:
       KVCACHE_POOL_ID
       KVCACHE_SLICE_NAME
       KVCACHE_ENDPOINT
       KVCACHE_TRANSPORT=tcp

LLM container
  └─ configures its engine-side LMCache connector using KVCACHE_ENDPOINT
```

## Core concepts

- **`KVCachePool`**: cluster-scoped CR representing a logical shared KV cache,
  such as a vLLM Llama 3 FP16, 16-token-block DRAM pool.
- **`KVCachePoolConfig`**: opaque config inside a DRA claim that describes the
  pool a workload needs.
- **Compatibility keys**: `engine`, `modelFamily`, `dtype`,
  `blockSizeTokens`, and `latencyTier`.
- **`capacityBytes`**: per-claim requested allocation. It is not a physical
  byte partition until the controller enforces capacity accounting.
- **`status.endpoint`**: the authoritative LMCache service address, written by
  the controller.
- **DRA `ResourceSlice` device**: a virtual node-local scheduling slot. It is
  distinct from the logical cluster-wide `KVCachePool`.

## Work completed

### API and generated clients

- Added a cluster-scoped `KVCachePool` API with compatibility fields, total
  `capacityBytes`, and readiness, endpoint, and allocated-byte status.
- Added `KVCachePoolConfig` for claim-side parameters.
- Generated typed Kubernetes clients, fakes, informers, and listers for
  `KVCachePool`.

### Kubelet plugin

- Removed unrelated NVIDIA GPU/NVML dependencies from the KV-cache CDI path.
- Implemented CDI environment-variable injection.
- The plugin prepares a CDI device containing pool name, slice name, endpoint,
  and TCP transport.
- The plugin reads `KVCachePool.status.endpoint` and requires the pool to be
  `Ready`.

### Match-or-provision ownership

The kubelet plugin does not make a cluster-wide pool match decision. It
resolves a selected pool by either:

1. Explicit `poolName` in `KVCachePoolConfig`, or
2. The controller-written `ResourceClaim` annotation:
   `kvcache.nvidia.com/kvcache-pool-name`.

The plugin then gets that pool, validates readiness, and injects its endpoint.
This maintains the intended v0 separation of responsibilities:

| Component | Responsibility |
| --- | --- |
| Controller | Match compatible pools, provision missing pools, bind claims, manage LMCache, publish status |
| Kubelet plugin | Consume a controller binding and inject runtime settings |
| LMCache / engine connector | Store, index, evict, read, and write KV blocks |

## Work remaining

### Controller (critical)

`cmd/kvcache-controller/` does not exist yet. It needs to:

1. Watch relevant `ResourceClaim`s and `KVCachePool`s.
2. Decode `KVCachePoolConfig`.
3. Match an existing compatible pool, honoring explicit `poolName`.
4. Create a `KVCachePool` if none matches.
5. Create and reconcile an LMCache workload and Service for new pools.
6. Set `KVCachePool.status.endpoint` and `status=Ready`.
7. Bind the claim with `kvcache.nvidia.com/kvcache-pool-name`.
8. Track and release `allocatedBytes` if v0 includes capacity enforcement.

Until it exists, automatic match-or-provision is unavailable. Demonstrations
must use an explicit `poolName` referring to a pre-created Ready pool.

### DRA resource publication (critical)

The kubelet plugin must publish `ResourceSlice` data, including a virtual
`kvcache-slot` device, so the scheduler can allocate claims to this driver.

### Deployment and integration

- Generate and ship the `KVCachePool` CRD.
- Add RBAC for the plugin and controller.
- Add kubelet-plugin DaemonSet and controller Deployment manifests.
- Define DeviceClasses, initially one per engine.
- Define an LMCache Deployment/Service template or generated workload.
- Add example `ResourceClaim` and pod manifests.
- Wire engine startup to the injected endpoint:
  - vLLM can use an existing LMCache connector.
  - TRT-LLM and SGLang need adapters or bridges.

### Testing

- Unit tests for matching, provisioning, binding, and readiness transitions.
- Plugin tests for explicit pool names, controller bindings, missing bindings,
  NotReady pools, and malformed configurations.
- End-to-end validation that two compatible pods receive the same endpoint and
  share cache hits through LMCache.

## Data-plane interface layer and Rust

There are two separate interface layers:

1. **Control plane**: the Kubernetes controller reads/writes `KVCachePool`
   objects, selects pools, provisions LMCache, and binds claims. Go is the
   lower-friction choice for this repository and the Kubernetes controller
   ecosystem, though Rust is technically viable with `kube-rs`.
2. **Data plane / engine interface**: code in or beside vLLM, TRT-LLM, or
   SGLang consumes `KVCACHE_ENDPOINT` and translates KV-cache operations into
   LMCache operations. This layer can be written in Rust if the target engine
   exposes an appropriate connector or plugin boundary.

For v0, vLLM should use its existing LMCache connector. Rust is most useful
for a standalone high-performance data-plane client, proxy, cache index, or
transport service. It is not necessary for CDI injection or basic Kubernetes
reconciliation.
