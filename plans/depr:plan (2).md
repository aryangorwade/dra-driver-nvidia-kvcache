# Kubernetes-Native Distributed KV Cache Primitive — Plan

> Status: Proposal / pre-design
> Owner: (you)
> Last updated: 2026-04-27
> Scope: NVIDIA-only (extend `dra-driver-nvidia-gpu`)

---

## 1. Executive summary

A **DRA-shaped Kubernetes primitive** for distributed KV cache pools, implemented as an extension to NVIDIA's `dra-driver-nvidia-gpu`, with reference data-plane adapters to LMCache (vLLM) and Dynamo workers (via NIXL).

The contribution is **not** a new KV cache data plane. The data planes already exist (LMCache, NIXL, Dynamo's KV manager, AIBrix's pool, llm-d's KV Cache Manager). The contribution is the **K8s-native resource model and scheduler integration above them** — the layer that lets any K8s workload claim KV pool capacity through the standard K8s API, with normal K8s mechanics for quota, RBAC, scheduling, and observability.

---

## 2. Current state

### Existing solutions (all "K8s-deployed", none "K8s-native" in the strict sense)

| System | Shape | What it does | What's missing |
|---|---|---|---|
| **LMCache** | Library + service | KV offload, prefix cache, NIXL/RDMA support, vLLM connector | Not a K8s primitive. Pods consume via env/sidecar. K8s API has no concept of it. |
| **NVIDIA Dynamo KV manager** | Internal to Dynamo | Cross-worker KV pool, NIXL transport, KV-aware routing | Bound to Dynamo workers. Non-Dynamo workloads cannot claim. |
| **llm-d KV Cache Manager** | Internal to llm-d | KV-aware routing, KV offload, multi-vendor stack | Bound to llm-d's worker contract. Non-llm-d workloads cannot claim. |
| **AIBrix KV pool** | Internal to AIBrix | Distributed KV pool with K8s-deployed components | Coupled to AIBrix gateway/engine. |
| **AWS HyperPod inference operator** | AWS-coupled operator | Workload orchestration, recently integrates LMCache | AWS-internal model. KV is not exposed as a K8s resource. |
| **vLLM KV connector API** | Engine-side hook | Pluggable KV offload | Engine API, not K8s API. |

### What does **not** exist

- A K8s **resource type** for KV cache pools.
- A way for a `NIMService`, an `llm-d` worker, and a standalone TRT-LLM `Deployment` to claim slices of the same pool through one API.
- K8s scheduler awareness of KV pool reachability when placing pods.
- `ResourceQuota` for KV pool bytes per namespace.
- RBAC over who can claim from which pool.
- Atomic composition of GPU + KV slice + NIC bandwidth in a single claim.
- A vendor-neutral provider contract that LMCache, NIXL, llm-d, AIBrix, etc., can all register into.

DRA reached GA in K8s 1.32 — recent enough that none of the existing systems were architected with it in mind. The absence is a timing gap, not a deliberate rejection.

---

## 3. Problem

**The distributed KV cache space is fragmented at the wrong layer.** Every existing implementation is vertically integrated with a specific serving stack. There is no Kubernetes-native abstraction that lets:

1. **Heterogeneous workloads** claim KV pool capacity uniformly. A multi-stack org running NIM + llm-d + standalone TRT-LLM today cannot share KV pool capacity across them. Each stack has its own pool or none.
2. **The Kubernetes scheduler** factor KV pool location into pod placement. Today, only stack-internal schedulers (Dynamo planner, llm-d scheduler) know about KV state. The K8s scheduler itself does not.
3. **Cluster admins** apply standard K8s mechanics to KV pools — quota, RBAC, audit, observability. KV capacity today is invisible to `ResourceQuota`.
4. **Operators** (NIM operator, KServe, etc.) declare "GPU + KV slice + NIC" as one atomic claim. Today it has to be glued together at the application layer.
5. **Pool implementations** be substitutable. There is no common contract that LMCache, NIXL, etc., implement against.

This is the same fragmentation pattern that storage, networking, service discovery, and routing went through before each got a K8s-native primitive (CSI, CNI, Service, Gateway API / EPP). KV cache is the conspicuously empty cell.

---

## 4. Solution

### 4.1 The primitive

Two new K8s API types, defined as DRA resources:

- **`KVCachePool`** — a cluster-scoped resource representing a pool of KV cache capacity advertised by a data-plane provider. Has rich attributes: capacity, latency tier, RDMA endpoint, model-family compatibility, locality info.
- **`KVCacheClaim`** — namespaced; a workload's request for a slice of pool capacity matching some `DeviceClass`.

### 4.2 DeviceClass taxonomy

DeviceClasses partition pools by **data-plane compatibility** — i.e., what bytes are interoperable.

```
kvcache.nvidia.com/lmcache-vllm-llama3-8b-fp16-bs16-tp1
kvcache.nvidia.com/lmcache-vllm-llama3-70b-fp8-bs16-tp4
kvcache.nvidia.com/nixl-trtllm-llama3-8b-fp16-bs16-tp1
kvcache.nvidia.com/nixl-dynamo-vllm-bs16
...
```

Lean strict on granularity. False sharing (incompatible bytes in the same class) is much worse than too-many-classes.

### 4.3 Two layers of sharing

| Layer | What it is | Who owns it |
|---|---|---|
| **Capacity** (Layer 1) | Multiple workloads coexist in one pool's byte budget | The primitive (DRA driver allocates slices) |
| **Block content** (Layer 2) | Two workloads share KV blocks when prefix-hashes match | Data plane (LMCache, NIXL — content-addressable) |

The primitive does **not** do hashing, dedup, or eviction. Those belong to the data plane.

### 4.4 Implementation: extend `dra-driver-nvidia-gpu`

- New resource type sibling to `ComputeDomain`.
- Reuse existing scaffolding (ResourceSlice publishing, kubelet plugin, claim allocator, CDI plumbing).
- Adds `KVCachePool` advertisement and bind-time wiring (RDMA endpoint, env vars, peer mapping).
- NVIDIA-only is acceptable scope. Vendor-neutrality lives in the *resource model contract*, not the driver implementation.

### 4.5 Adapters

Engine-specific code that translates between the engine's native KV API and the primitive's contract.

- **vLLM** (via LMCache connector API). First reference adapter. Cleanest, most stable, easiest.
- **Dynamo workers** (via NIXL). Second reference adapter. Different shape (C++/native), proves genericity.
- **TRT-LLM / NIM**. Documented design, deferred implementation. NVIDIA-internal access can pull this in earlier.
- **SGLang, others**. Future work, written by their respective maintainers against the contract.

### 4.6 Architecture

```
┌──────────────────────────────────────────────────────────────┐
│  Workloads (any K8s pod)                                      │
│  vLLM       Dynamo workers     SGLang     NIM     KServe      │
└────────┬───────────┬───────────────┬────────┬────────┬───────┘
         │ KVCacheClaim                                          
         ▼                                                       
┌──────────────────────────────────────────────────────────────┐
│  K8s primitive: KVCachePool / KVCacheClaim (DRA-shaped)       │
│  - one resource model, scheduler-aware                        │
│  - quota, RBAC, observability, audit                          │
│  - atomic composition with GPU/NIC claims                     │
└────────┬───────────────────────────────────────────┬─────────┘
         │ providers register                                    
         ▼                                            ▼          
┌──────────────────────┐                  ┌────────────────────┐
│  Data plane:          │                 │  Data plane:        │
│  LMCache pool         │                 │  NIXL / Dynamo KV   │
│  (CPU/disk/RDMA tier) │                 │  manager            │
└──────────────────────┘                  └────────────────────┘
```

The primitive sits one layer above the data planes, one layer below the workloads.

---

## 5. Why it's worth solving

Concrete capabilities the primitive unlocks that don't exist today:

| Capability | Concreteness | Beneficiary |
|---|---|---|
| Cross-stack pool sharing (NIM + llm-d + standalone TRT-LLM through one API) | High | Multi-stack platform teams |
| K8s scheduler factors KV pool reachability into placement for any workload | High | Platform teams, latency-sensitive workloads |
| `ResourceQuota` for KV bytes per namespace | High | Cluster admins (genuinely missing today) |
| RBAC over claims | High | Cluster admins |
| Operator-level atomic composition (GPU + KV + NIC) | High | NIM operator, KServe, custom in-house operators |
| Standard K8s observability surfaces | Medium | Platform teams |
| Provider substitutability (LMCache vs. NIXL vs. AIBrix) | Medium — depends on adoption | Future engine implementers |

### The bet

Inference infrastructure follows the platformization pattern that storage, networking, service discovery, and routing have followed. The KV-cache primitive is the empty cell that pattern predicts.

### Counter-bet (if it doesn't pay off)

llm-d wins outright as the de facto K8s-native layer and absorbs the role. Acceptable downside: the standalone artifact is still a working DRA driver extension with two adapters and benchmark data — portfolio-grade regardless of whether the standardization bet lands.

### Hedge

Propose the resource-model contract to llm-d concurrently. If llm-d adopts it, the primitive lands in the canonical multi-vendor place. If not, the standalone artifact stands.

---

## 5a. How the wins actually materialize (cross-stack mechanisms)

The cross-stack story is conditional on **`DeviceClass` compatibility**. Different stacks (Dynamo + standalone TRT-LLM, NIM + llm-d, etc.) only share KV bytes when their KV format matches: same model, dtype, block size, attention variant, tokenizer. When that holds, byte-level sharing through a pool becomes possible — which today it isn't, because each stack's pool is internal.

Six distinct mechanisms, with different requirements:

| # | Mechanism | Requires class match? | Benefit |
|---|---|---|---|
| 1 | Block-level sharing across stacks | Yes | Zero re-prefill on prefix hits across stacks. Direct latency + compute savings. |
| 2 | Capacity sharing without byte sharing | No | Pool capacity flows to whichever workload needs it. Operational efficiency. |
| 3 | Cold-start warming for new pods | Yes | Scale-up events inherit the cluster's hot prefix set instead of starting cold. |
| 4 | Spillover capacity (DRAM/CXL/RDMA tier) | No | Workloads support longer contexts / more sessions than fit in local HBM. |
| 5 | Disaggregated prefill/decode coordination | Yes | Prefill workers write to pool; decode workers read. Today only in Dynamo; primitive opens it to non-Dynamo stacks. |
| 6 | Routing co-placement (with EPP) | No | Router places requests near the pool with their hot blocks. |

**The strongest cross-stack inference win (Mechanism 1) is conditional on compatible architectures.** Practical multi-stack environments will have *some* configurations in common — both stacks running the same model family for some products even if they diverge elsewhere — and the primitive captures the win on those cases. Mechanism 2 (capacity sharing) holds regardless of class match.

---

## 5b. Performance characterization (it can go either way)

The primitive is a tool, not a default. It can both improve and harm inference performance depending on workload profile and infrastructure. The plan must measure and document both directions.

### Where it speeds up

- **Long shared prefixes + fast fabric + large models.** Prefill compute drops 30–80% on shared content; TTFT drops 15–35% (published numbers from RadixAttention, Preble, vLLM APC).
- **Larger effective KV capacity** via spillover tier (DRAM/CXL/RDMA-attached). Longer contexts and more concurrent sessions without adding GPUs.
- **Faster cold starts.** New pods warm from the pool instead of recomputing prefill.
- **Cluster-side wins.** Higher GPU utilization, better bin-packing, multi-tenant capacity sharing, lower aggregate KV bytes, autoscaling resilience (cache survives scale events).

### Where it slows down

- **Cache miss latency vs. recompute latency.** Pool reads cost ~10–100 µs over RDMA, ~hundreds of µs–ms over TCP. Prefill is ~100 µs–1 ms per block. For small models on fast GPUs over slow networks, **recomputation can be cheaper than fetching**. This is the primary risk.
- **Bind-time + scheduling overhead.** Adds ~100ms–1s to pod start. Doesn't affect steady-state inference latency. Negligible in practice.
- **Eviction interference.** Multi-tenant pools may evict one workload's blocks under another's pressure; victim then misses on its own prefix and recomputes.
- **Network contention.** A pool sharing a fabric with NCCL collectives competes for bandwidth.

### When *not* to use the primitive

- Single-pod deployments — no cross-pod benefit; pure overhead. In-engine APC is strictly better.
- Small models on fast GPUs over slow networks — recomputation beats fetching.
- Workloads with no prefix locality (random/unique prompts).
- Latency-strict workloads with cold pools — first-request miss can be slower than just running prefill.

The primitive is opt-in via `ResourceClaim`. Workloads outside the profile should not claim. v1 must not silently route through the pool by default.

### What the benchmark must establish

Phase 5 explicitly characterizes the inflection points: at what model size, fabric speed, prefix length, and locality does the primitive transition from net-positive to net-negative? Honest negative-result regimes are part of the artifact, not a flaw in it.

---

## 5c. Compatibility tiers (which deployments share a `ResourceClaim`)

"Same `ResourceClaim`" means matching `DeviceClass` selector → bound to the same pool → byte-level KV sharing possible. The compatibility tuple is `(model, dtype, block_size, attention_variant, tokenizer, engine_family, version_band, TP topology)`.

### Tier 1 — Same engine, same config: **YES**

| Scenario | Same claim? |
|---|---|
| Two vLLM deployments, same model + dtype + block size | Yes |
| Two TRT-LLM deployments, matched build config | Yes |
| Two SGLang deployments, matched config | Yes |
| Two Dynamo deployments, same underlying engine + config | Yes |

Bulk of practical value.

### Tier 2 — Engine + wrapper, same underlying engine: **YES**

The wrapper doesn't change KV format. What matters is the underlying engine.

| Scenario | Same claim? |
|---|---|
| Standalone vLLM + Dynamo running vLLM workers (same vLLM config) | Yes |
| Standalone TRT-LLM + Dynamo running TRT-LLM workers (same build) | Yes |
| Standalone TRT-LLM + NIM with TRT-LLM image (same build) | Yes |
| Standalone vLLM + NIM with vLLM image (same vLLM config) | Yes |
| Standalone vLLM + llm-d running vLLM workers (same config) | Yes |
| llm-d running vLLM workers + Dynamo running vLLM workers (same config) | Yes |
| NIM with vLLM + AIBrix with vLLM (same config) | Yes |

The **interesting cross-stack case**. Different K8s-native serving stacks (Dynamo, llm-d, NIM, AIBrix), all running vLLM under the hood with matched config, all file the same `KVCacheClaim` and bind to the same pool.

### Tier 3 — Cross-engine with deliberate alignment: **NO out of the box**

Different `DeviceClass` by default. Same-claim only with engine-side adapters that re-layout on read/write. Real work; not v1.

| Scenario | Same claim? |
|---|---|
| vLLM + SGLang (matched block size + tokenizer) | No — different layout, needs translation |
| vLLM + TGI | No |
| vLLM + LMDeploy | No |

### Tier 4 — Generally incompatible: **NO**

Different `DeviceClass`. Different pools. Cluster-level capacity may still be shared (Mechanism 2); bytes are not.

| Scenario | Same claim? |
|---|---|
| vLLM + TRT-LLM (different families) | No |
| Anything + llama.cpp | No |
| Anything + MLC-LLM, DeepSpeed-MII | No |
| Llama-3-MHA + Llama-3-GQA (different attention variants) | No |
| FP16 + FP8 deployment of same model | No |
| TP=2 + TP=4 deployment of same model (in most engines) | No |

### Summary

Tiers 1 and 2 give byte-level sharing. Tier 3 needs translation (future work). Tier 4 doesn't share bytes regardless. The value-bearing cases are **Tier 1 + Tier 2**, specifically the **vLLM-underneath group** (vLLM + Dynamo+vLLM + llm-d+vLLM + NIM+vLLM + AIBrix+vLLM) — the largest realistic compatibility cluster, and where the cross-stack benchmark should land.

### How likely is shared-claim sharing in practice?

Honest assessment:

- **Accidental** sharing (two unrelated teams happen to pick matching configs) is **rare**. Teams independently tune `block_size`, dtype, TP, model variants, and engine versions for their own targets. Don't bank on it.
- **Deliberate** sharing (platform team standardizes on a small set of canonical configs and requires workloads to use them) is **common in any serious cluster**. This is how inference is run at scale because the alternative is operationally chaotic.
- **Cross-stack** sharing (Tier 2) requires the underlying engine to match (very common — vLLM is the dominant substrate) **and** configs to match across stacks (requires deliberate alignment). Real but requires platform discipline; not free.

The framing: the primitive does **not** manufacture sharing opportunities. It captures sharing opportunities that platform standardization creates. A cluster with no standardization sees small wins (capacity sharing, Mechanism 2). A cluster with standardization sees the full benefit (byte sharing, Mechanism 1 + cold-start, Mechanism 3 + disagg, Mechanism 5).

For 2026 inference clusters at platform-scale orgs, where vLLM is the dominant substrate and standardization is the norm, **moderate-to-high likelihood** that real deployments land in shared pools. Less common in clusters with no platform team or no inference-config governance.

---

## 5d. Does it require similar conversations?

No — but the **largest** wins do come from prefix similarity. The benefits split cleanly:

| Benefit | Requires prefix/content similarity? |
|---|---|
| Prefill compute reduction (byte-level hits) | Yes |
| Lower TTFT from cache hits | Yes |
| Cold-start warming | Mostly yes |
| Spillover capacity (longer context, more sessions) | No |
| Cluster capacity sharing (Mechanism 2) | No |
| Disaggregated prefill/decode | No |
| Quota / RBAC / scheduler / observability | No |

The **inference-speedup story** is similarity-dependent. The **infrastructure story** isn't.

In practice, most production traffic has more similarity than initially assumed:
- System prompts → 100% locality on the prefix.
- Multi-turn chat → per-session locality.
- RAG → shared doc chunks.
- Agent / tool-use loops → identical tool/few-shot scaffolding.
- Eval / batch jobs → template reuse.

Genuinely zero-similarity workloads exist (single-shot creative writing, etc.) but are not dominant.

Phase 5 must include a low-similarity workload as a control to characterize the floor.

---

## 6. Out of scope

- **Cross-engine byte-level interop.** Different engines use different KV layouts. The primitive does not standardize bytes. DeviceClass-per-format is the boundary.
- **A new KV data plane.** LMCache, NIXL, etc., already exist and are reused.
- **A new inference engine, scheduler, or runtime.** This is a resource-model layer.
- **AMD / Intel / Habana support.** NVIDIA-only is the explicit scope.
- **Distributed cache coherence protocols.** Owned by the data plane.
- **KV format standardization.** Multi-org, multi-year effort outside this project.

---

## 7. Risks & mitigations

| Risk | Mitigation |
|---|---|
| llm-d absorbs the standardization role first | Hedge: propose contract to llm-d concurrently. Even if absorbed, the DRA artifact still ships. |
| NVIDIA driver maintainers reject scope expansion (KV in GPU driver) | Fall back: fork scaffolding into sibling repo (`dra-driver-nvidia-kvcache`). Same code, separate tree. |
| vLLM KV connector API churns | Pin a version; minimize surface area touched; contribute upstream stability fixes if needed. |
| No RDMA hardware access | Develop on TCP/loopback; rent CoreWeave/Lambda for benchmark phase only (1–3 weeks). |
| Half-finished projects don't demo well | Each phase ends with a deliverable that stands alone. Design doc → scaffolding → vLLM adapter → benchmark — each is independently presentable. |
| Engine-config matrix explodes (TP, dtype, block size, attention variant) | Lean strict on DeviceClass granularity; document the schema; defer compatibility unification as future work. |
| Block-sharing benefit is small on real workloads | Pick workloads with known prefix locality (system-prompt-heavy, multi-turn, RAG). Document negative results honestly if they appear. |

---

## 8. Roadmap (weekly, part-time ~10–15 hrs/week)

Total: ~22–26 weeks. Calendar buffer assumed; slip is normal.

### Phase 0 — Setup & ramp (Weeks 1–2)
- W1: Read `dra-driver-nvidia-gpu` source end-to-end. Understand `ComputeDomain` pattern.
- W1: Read LMCache architecture and vLLM KV connector API.
- W2: Stand up a local kind/k3d cluster running the existing NVIDIA DRA driver (no GPU on local; stub).
- W2: Pick benchmark workload candidates (LMSYS-Chat-1M, ShareGPT, system-prompt synthetic).

**Exit criteria:** Local dev environment works; reading-list complete.

### Phase 1 — Design doc (Weeks 3–5)
- W3: Resource model — `KVCachePool`, `KVCacheClaim`, attribute schema.
- W4: DeviceClass taxonomy and granularity decisions.
- W5: Adapter contract; mapping exercise for vLLM, Dynamo (NIXL), TRT-LLM.

**Exit criteria:** Design doc reviewable by maintainers. Submit as discussion in driver repo (or NVIDIA-internal equivalent) for early feedback.

### Phase 2 — DRA driver extension (Weeks 6–8)
- W6: Add new DeviceClass + ResourceSlice publisher for `KVCachePool` (stub data).
- W7: Claim allocator wiring and basic admission.
- W8: Kubelet plugin hooks (`NodePrepareResource` / `NodeUnprepareResource`) — env injection, dummy endpoint.

**Exit criteria:** A pod with a `KVCacheClaim` gets bound and starts; receives endpoint info as env vars. Data plane still stubbed.

### Phase 3 — LMCache data-plane integration (Weeks 9–12)
- W9: Stand up LMCache as a real Service. Verify multi-pod connectivity.
- W10: Wire driver to advertise actual LMCache pool capacity.
- W11: Engine-side adapter — vLLM connector configured to talk to LMCache via primitive-bound endpoint.
- W12: End-to-end smoke test: two vLLM pods sharing one LMCache pool through the primitive.

**Exit criteria:** Two pods, one pool, observable cross-pod block sharing on a shared prefix.

### Phase 4 — Second adapter: Dynamo / NIXL (Weeks 13–16)
- W13: NIXL transport bring-up.
- W14: Dynamo worker adapter — register NIXL pool as a provider.
- W15: Dynamo worker pod claims through primitive; binds successfully.
- W16: Cross-class isolation test: vLLM-class workload + Dynamo-class workload coexist; capacity admin uniform; bytes class-segregated.

**Exit criteria:** Two adapters of meaningfully different shape both working through the primitive. Genericity demonstrated.

### Phase 5 — Benchmark (Weeks 17–20)
- W17: Provision multi-node GPU + RDMA cluster (CoreWeave / Lambda / similar) for measurement window.
- W18: Workload generation — system-prompt-heavy synthetic, LMSYS-Chat-1M slice, multi-turn.
- W19: Measurements: TTFT, ITL, prefill compute reduction, hit rate, fairness, behavior under autoscaling churn.
- W20: Tear down rented cluster. Analyze results.

**Exit criteria:** Clean comparison plots: with-primitive vs. without; per-pod APC vs. shared-pool; cross-stack vs. single-stack.

### Phase 6 — Writeup & upstream (Weeks 21–24)
- W21: Design doc → blog post / arXiv-style report.
- W22: Upstream PR #1 — DeviceClass scaffolding (small, easy to review).
- W23: Upstream PR #2 — substantive implementation, motivated by benchmark.
- W24: Submit cross-vendor proposal to llm-d and Gateway API Inference Extension communities.

**Exit criteria:** Public artifact (writeup), open PRs, public proposal threads.

### Buffer (Weeks 25–26)
Slip absorption. Use for review cycles, follow-ups, or extending whichever phase ran short.

---

## 9. Extension framework — how this work continues

After v1, the project extends along five axes. Each is independently scoped.

### 9.1 New adapters
- **TRT-LLM / NIM.** Highest priority; NVIDIA-internal access accelerates this. Slot into existing contract; no design changes required.
- **SGLang.** Maintainers can write their own adapter against the published contract.
- **Other engines.** Same path.

**Contract for adding an adapter:**
1. Implement the engine-side connector against the primitive's bind-time API (endpoint, capacity, class).
2. Add a `DeviceClass` matching the engine's compatibility tuple.
3. Add a smoke test demonstrating bind + write + read on the engine's KV pages.

### 9.2 New data-plane providers
- **AIBrix pool** as a registered provider.
- **Dynamo's KV manager** as a registered provider (separate from worker-side NIXL adapter — this exposes the pool itself).
- **CXL-attached memory tier** when hardware lands.
- **GPU-direct storage backed pools** (NVMe over RDMA).

**Contract for adding a provider:**
1. Implement `KVCachePool` advertisement on the driver side.
2. Map provider-specific lifecycle to `NodePrepareResource` / `NodeUnprepareResource`.
3. Declare which `DeviceClass`es the provider's pools satisfy.

### 9.3 Resource-model evolution
- **Soft reservations vs. hard claims.** Today: hard binding. Future: hint-based co-placement when full binding isn't required.
- **Tier-aware claims.** Pods declare latency budgets; driver picks among hot/warm/cold pools.
- **Cross-namespace pool sharing** with explicit ACLs.
- **Multi-cluster federation** of pools (longer horizon).

### 9.4 Scheduler integration depth
- v1: Standard DRA scheduler integration (node-fit on attributes).
- v2: Custom scheduler plugin for prefix-hot-set scoring (when prefix index becomes a primitive attribute).
- v3: Co-scheduling with Gateway API Inference Extension EPP (router and scheduler share signals).

### 9.5 Upstream / standardization
- Land DRA extension in `dra-driver-nvidia-gpu`.
- Propose contract to llm-d as a cross-vendor adoption play.
- Propose KEP-shaped discussion in sig-node about whether KV pool deserves an upstream resource type.
- Coordinate with vLLM maintainers on KV connector API stability.

### 9.6 Standalone Driver Extraction
While v0 is built as a fork of `dra-driver-nvidia-gpu` for rapid prototyping—gaining Kubernetes DRA gRPC scaffolding, CDI injection, and checkpoint state management for free—the final goal is a standalone, vendor-neutral repository.
- Keep the KV cache Go modules (`cmd/kvcache-*` and `api/`) strictly isolated from GPU, NVML, and IMEX logic during development.
- Once v0 is proven, port these modules along with the generic Kubernetes boilerplate (copied from `pkg/workqueue`, `pkg/flock`, etc.) into a fresh repository (e.g., `github.com/aryangorwade/dra-driver-kvcache`).
- This transitions the project from an NVIDIA-specific extension to a cross-vendor (AMD/NVIDIA/Intel) Kubernetes primitive.

---

## 10. Prerequisites & dependencies

### Hardware
- Local: any dev machine for K8s control-plane work (kind/k3d).
- Phase 3+: at least one multi-GPU node (rented or owned) for vLLM/LMCache integration.
- Phase 4: NVLink or single-node RDMA acceptable for development.
- Phase 5: multi-node RDMA cluster for ~3 weeks (CoreWeave, Lambda Cloud, Crusoe, Nebius). Rent, don't own.

### Software
- K8s 1.32+ (DRA GA).
- Go (strong fluency assumed).
- Python (for vLLM adapter, LMCache wrapping, benchmark harness).
- vLLM (current stable).
- LMCache (current stable).
- NIXL (Dynamo's transport library).

### Access
- NVIDIA-internal: TRT-LLM source, Dynamo internals, NIXL docs/source. Significantly accelerates Phases 4 and (optionally) a TRT-LLM adapter.
- External: same project shape, with deferred TRT-LLM and slower NIXL ramp.

---

## 11. Open questions

- **Driver tree decision.** Land in `dra-driver-nvidia-gpu` or fork to `dra-driver-nvidia-kvcache`? Resolve in Phase 1.
- **DeviceClass naming convention.** Settle in Phase 1 design doc. Strict tuple-based naming preferred.
- **Eviction policy surface.** Does the primitive expose hints (priority, no-evict) to the data plane, or stay fully hands-off? Lean hands-off for v1; revisit in v2.
- **Benchmark workload specifics.** Settle in Phase 0 (week 2). LMSYS-Chat-1M is the strong default.
- **Multi-tenant isolation guarantees.** v1 is best-effort (data plane handles eviction). Hard guarantees deferred.

---

## 12. Definition of done (v1)

- DRA extension to `dra-driver-nvidia-gpu` (or sibling repo) with `KVCachePool` and `KVCacheClaim` types.
- Two working adapters of meaningfully different shape: vLLM-via-LMCache, Dynamo-via-NIXL.
- Benchmark on multi-node hardware with RDMA, on at least one workload with measurable cross-pod KV-block sharing benefit.
- Public writeup describing the resource model, the bet, the limitations, and the measurements.
- Upstream PR(s) submitted (acceptance not required for v1 done; submission is).
- Cross-vendor design proposal posted to llm-d / Gateway API Inference Extension community.

Anything beyond this is v2.

---

## 13. Long-horizon note: cross-format KV interop (v3+)

The v1 primitive partitions pools by `DeviceClass` because KV bytes are engine-format-specific (block size, tensor layout, dtype, attention variant, tokenizer). A natural future direction is **cross-format interop within a single pool** — letting a prefix prefilled by one engine be consumed by another.

Two flavors, ranked by tractability:

- **Format coexistence (preferred for v3).** The pool stores the same logical prefix in multiple format-specific blobs, indexed by `(content_hash, format_tag)`. The first engine to prefill populates its format; later engines populate theirs. No runtime translation. Storage cost up, translation cost zero. The primitive's API barely changes — pools just advertise multiple DeviceClasses and cache multiple representations internally.
- **True format translation (research-grade, not recommended).** Translate blocks between formats on the fly. Hard or impossible across model/tokenizer/attention-variant boundaries; even within a narrow envelope (same model, same dtype, same tokenizer, same attention variant), translation cost can approach recomputation cost. Multi-year research effort; explicitly out of scope.

The v1 design does not preclude either direction. Revisit only after adapter coverage and multi-tenant isolation are solid. Document, don't build.
