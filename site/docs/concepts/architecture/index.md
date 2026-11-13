---
id: architecture
title: Architecture
sidebar_position: 2
---

# Architecture

This section provides a detailed look at the architectural components of Agent Router. Understanding the architecture will help you better deploy, configure, and maintain your gateway installation.

## Overview

Agent Router follows a modern cloud-native architecture with distinct control and data planes. This separation of concerns allows for better scalability, maintainability, and flexibility in deployment options.

Agent Router integrates with Envoy Gateway for the control plane and Envoy Proxy for the data plane.

## Key Concepts

### Control Plane

A control plane is a component that manages the configuration of the data plane. We utilize Envoy Gateway as a central control plane, and Agent Router works in conjunction with it to manage the data plane configuration.

### Data Plane

The data plane is the component that sits in the request path and processes the requests. In the context of Agent Router, the data plane consists of Envoy Proxy and the AI Gateway external processor that processes the AI requests.

## In This Section

1. [System Architecture Overview](./system-architecture.md)
   - High-level architecture overview
   - Control and data plane separation

2. [Control Plane Explained](./control-plane.md)
   - AI Gateway Controller
   - Envoy Gateway Controller
   - Configuration management
   - Resource orchestration

3. [Data Plane and Traffic Flow](./data-plane.md)
   - External Processor functionality
   - Request processing flow

## What's Next

After understanding the architecture:

- Check out our [Getting Started](../../getting-started/index.md) guide for hands-on experience
