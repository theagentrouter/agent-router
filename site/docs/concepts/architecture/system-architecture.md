---
id: system-architecture
title: System Architecture Overview
sidebar_position: 1
---

# System Architecture Overview

Agent Router follows a modern cloud-native architecture pattern with distinct control and data planes. Let's explore how these components work together to manage AI traffic effectively.

The architecture is divided into two main planes:

1. **Control Plane**: Responsible for configuring and managing the system
   - Kubernetes API Server serves as the configuration interface
   - AI Gateway Controller manages AI-specific resources and configurations
   - Envoy Gateway Controller handles core proxy configuration and xDS

2. **Data Plane**: Handles the actual request traffic
   - Envoy Proxy and External Processor work together to handle AI traffic
   - Envoy Proxy routes and manages all incoming requests
   - External Processor handles AI-specific transformations and validations
   - Rate Limit Service manages token-based rate limiting

The Control Plane configures the Data Plane through several paths:

- AI Gateway Controller configures the External Processor as well as fine-tuning xDS configurations via Envoy Gateway extension server mechanism.
- Envoy Gateway Controller configures Envoy Proxy through xDS as well as Rate Limit Service.

```mermaid
graph TB
    accTitle: Agent Router system architecture
    accDescr: The control plane holds the Kubernetes API server, the AI Gateway controller and the Envoy Gateway controller. The AI Gateway controller reads AI Gateway resources from the API server, writes Gateway API and Envoy Gateway resources back, and fine-tunes Envoy Gateway's xDS. The Envoy Gateway controller reads Gateway API and Envoy Gateway resources. In the data plane, Envoy Proxy calls the rate limit service and the external processor. The AI Gateway controller configures the external processor; the Envoy Gateway controller configures the rate limit service and updates Envoy Proxy's config.

    subgraph "Control Plane"
        K8s[Kubernetes API Server]
        Controller[AI Gateway Controller]
        EGController[Envoy Gateway Controller]

        Controller --> |GWAPI/EG resources|K8s
        K8s -->|AI Gateway resources| Controller
        K8s -->|GWAPI/EG resources| EGController
        Controller <-->|xDS fine-tune| EGController

        style Controller fill:#f9f,stroke:#333,stroke-width:2px,color:#12100E
        style EGController fill:#ddf,stroke:#333,color:#12100E
    end

    subgraph "Data Plane"
        Envoy[Envoy Proxy]
        ExtProc[External Processor]
        RateLimit[Rate Limit Service]

        Envoy --> RateLimit
        Envoy --> ExtProc

        style Envoy fill:#f9f,stroke:#333,stroke-width:2px,color:#12100E
        style ExtProc fill:#f9f,stroke:#333,stroke-width:2px,color:#12100E
        style RateLimit fill:#dfd,stroke:#333,color:#12100E
    end

    Controller -.->|Configures| ExtProc
    EGController -.->|Configures| RateLimit
    EGController -.->|Updates Config| Envoy
```

For detailed information about specific components:

- Learn more about the [Control Plane](./control-plane.md) components
- Understand the [Data Plane](./data-plane.md) in detail
