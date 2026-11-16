# Proposal: Scaling Dynamic Configuration Updates via API and xDS

## 1. Motivation

As the Envoy AI Gateway (AIGW) is adopted in highly multi-tenant environments, the scale and frequency of configuration updates increase significantly. 
Currently, the AIGW Controller watches Kubernetes Custom Resource Definitions (CRDs), reconciles them into an internal configuration representation, and writes the output to a Kubernetes Secret. The AIGW `ext_proc` (and Envoy) then consumes this Secret.

While this declarative approach works well for small to medium-sized deployments, it introduces bottlenecks at scale:
1. **Kubernetes API Server Limits:** High frequency of tenant config changes places significant strain on the K8s API server and `etcd`.
2. **Secret Size Constraints:** Kubernetes Secrets have a strict 1MB size limit. A deployment with tens of thousands of tenants and complex routing rules can easily exceed this.
3. **Propagation Latency:** The pipeline of writing a CRD -> Controller reconciliation -> writing to a Secret -> kubelet syncing the Secret to the pod -> file watch triggers in `ext_proc` incurs a high end-to-end latency. 

To support real-time, highly scalable multi-tenancy, we need a mechanism to update routing and backend configurations dynamically without being bottlenecked by Kubernetes resource limits.

## 2. Proposed Architecture

We propose enhancing the AIGW Controller to support a dual-mode operation and transitioning the configuration distribution mechanism from Kubernetes Secrets to **xDS (Envoy's Universal Data Plane API)**.

### 2.1. Current vs. New Architecture Comparison

**Current Architecture (Mode 1: CRD Watcher & K8s Secrets)**

```text
 ┌───────────────┐     ┌───────────────┐     ┌───────────────┐     ┌───────────────────┐
 │               │     │               │     │               │     │                   │
 │ Tenant/GitOps ├───► │ K8s API Server├───► │AIGW Controller├───► │    K8s Secret     │
 │   (Applies    │     │   (& etcd)    │     │ (Reconciler)  │     │ (max 1MB limit)   │
 │     CRDs)     │     │               │     │               │     │                   │
 └───────────────┘     └───────────────┘     └───────────────┘     └─────────┬─────────┘
                                                                             │
                                                                       (kubelet sync
                                                                        file watch)
                                                                             │
                                                                   ┌─────────▼─────────┐
                                                                   │                   │
                                                                   │   AIGW ext_proc   │
                                                                   │                   │
                                                                   └───────────────────┘
```

**New Proposed Architecture (Mode 2: API Server & xDS)**

```text
 ┌───────────────┐                           ┌───────────────┐     ┌───────────────────┐
 │               │                           │               │     │                   │
 │External Client├─────────(REST/gRPC)──────►│AIGW Controller├─xDS►│   AIGW ext_proc   │
 │ / Config Mgr  │       (Bypasses K8s)      │ (API & xDS CP)│     │    (xDS Client)   │
 │               │                           │               │     │                   │
 └───────────────┘                           └───────────────┘     └───────────────────┘
```

### 2.2. Controller Modes of Operation

The AIGW Controller will support two distinct modes of operation to accommodate both GitOps-driven and dynamically-driven architectures:

*   **Mode 1: CRD Watcher (Current Mode)**
    The Controller operates as it does today, watching K8s CRDs (`AIGatewayRoute`, etc.), reconciling changes, and generating the necessary proxy configuration.
    
*   **Mode 2: API Server (New Push Mode)**
    The Controller exposes a dedicated **gRPC/REST endpoint**. External clients or centralized configuration managers can push configuration updates directly to the Controller. This completely bypasses the K8s API server and `etcd` for tenant-specific routing changes.

### 2.2. Configuration Distribution via xDS

Regardless of how the configuration is ingested (CRD vs. API push), the distribution of the consolidated configuration to the data plane will shift to xDS:

1.  **Controller as xDS Server:** 
    The AIGW Controller will implement an xDS control plane server (e.g., using the go-control-plane library). Whenever the configuration is reconciled (either from a CRD update or an API push), the Controller will push the updated configuration resources to the connected xDS clients.
    
2.  **Extproc as xDS Client:**
    The AIGW `ext_proc` server will be enhanced to act as an xDS client. Instead of watching a local file volume-mounted from a Secret, it will connect to the AIGW Controller's xDS server endpoint and subscribe to configuration updates (such as routing rules, backend definitions, and LLM specific policies).

## 3. Envoy Cluster Management & Dynamic Forward Proxy (DFP)

A critical concern in dynamic backend scaling is: *If the Controller pushes new backend definitions only to `ext_proc`, how does the Envoy proxy know how to route to these new backends without a predefined Envoy `Cluster`?*

This architecture leverages **Envoy's Dynamic Forward Proxy (DFP)** to solve this:

1. **Single DFP Cluster:** Envoy is configured statically (via Envoy Gateway) with a single DFP Cluster, rather than requiring a dedicated `Cluster` for every upstream LLM provider or tenant.
2. **Dynamic Authority Override:** When `ext_proc` evaluates a request and selects a backend (learned dynamically via the Controller's xDS push), it mutates the request headers (e.g., overriding `:authority` and `:path`).
3. **On-the-fly Resolution:** Envoy receives the mutated request from `ext_proc`, routes it to the DFP cluster, and dynamically resolves the DNS for the new `:authority` on the fly. 

This decoupling ensures that we can scale to tens of thousands of dynamic backends without ever needing to bloat Envoy's configuration memory with thousands of static `Cluster` definitions.

## 4. High-Level Flow

The operational flow for dynamic updates in **Mode 2 (API Push)** will be:

```text
 ┌────────────────┐         ┌─────────────────────────┐         ┌───────────────────┐
 │                │         │                         │         │                   │
 │ External Client├─(Push)─►│     AIGW Controller     ├─(xDS)──►│   AIGW ext_proc   │
 │ / Config Mgr   │  REST/  │  (API Server & xDS CP)  │         │    (xDS Client)   │
 │                │  gRPC   │                         │         │                   │
 └────────────────┘         └─────────────────────────┘         └─────────▲─────────┘
                                                                          │
                                                                      (ext_proc
                                                                        gRPC)
                                                                          │
                                                                ┌─────────▼─────────┐
                                                                │                   │
                                                                │       Envoy       │
                                                                │                   │
                                                                └───────────────────┘
```

1. **Push**: A tenant creates a new route. The Client pushes this change via gRPC/REST to the AIGW Controller.
2. **Reconcile**: The Controller updates its internal state in-memory (and optionally backs it up to a scalable external datastore if required, rather than K8s etcd).
3. **Distribute**: The Controller streams the updated config snapshot via xDS to connected `ext_proc` instances (for AI routing rules and policies) instantly. Envoy continues to receive its base routing configuration (like Gateway API HTTPRoutes) from its usual control plane (e.g., Envoy Gateway).
4. **Data Path**: Envoy receives a request and invokes the `ext_proc` gRPC API. `ext_proc` applies the latest dynamically loaded policies and routes the request.

## 4. Benefits

*   **Bypasses Kubernetes Limits:** Escapes the 1MB Secret size limit and offloads pressure from the K8s API server.
*   **Sub-second Latency:** xDS streaming ensures configuration updates are propagated to the data plane almost instantly.
*   **Standardization:** Moves closer to Envoy's native configuration delivery model (xDS), providing a strong foundation for future Envoy-specific dynamic configurations (like pushing direct RouteConfiguration updates).
*   **Flexibility:** Maintains backward compatibility with the existing CRD-based approach for users who prefer GitOps, while unblocking high-scale enterprise use cases.

## 5. Implementation Milestones

1. **Phase 1: Controller xDS Server & Extproc xDS Client**
   - Implement the xDS control plane inside the Controller.
   - Refactor `ext_proc` to consume configuration via xDS rather than file watchers.
   - *Result*: K8s Secrets are eliminated, but config is still sourced from CRDs.
   
2. **Phase 2: Controller API Mode**
   - Implement the gRPC/REST API on the Controller for config ingestion.
   - Add a configuration flag to switch between `crd` and `api` ingestion modes.
   
3. **Phase 3: High Availability & Persistence (Optional/Future)**
   - When running in API mode, ensure the Controller can persist state to an external DB (e.g., Redis, Postgres) for HA and crash recovery.
