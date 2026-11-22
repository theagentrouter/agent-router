# Copyright Envoy AI Gateway Authors
# SPDX-License-Identifier: Apache-2.0
# The full text of the Apache license is available in the LICENSE file at
# the root of the repo.

# Builds an Envoy image with the end-to-end test dynamic module at /usr/local/lib/libtestdynamicmodule.so.
#
# Dynamic modules are tied to an Envoy version: ENVOY_VERSION matches Envoy Gateway's default Envoy image, and the SDK
# version in go.mod matches ENVOY_VERSION, as in Envoy Gateway's own dynamic module test (examples/dynamic-module-test).
ARG GOLANG_BASE_IMAGE=golang:1.27
ARG ENVOY_VERSION=v1.40.2

FROM ${GOLANG_BASE_IMAGE} AS build
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=1 go build -buildmode=c-shared -o /build/libtestdynamicmodule.so .

FROM docker.io/envoyproxy/envoy:distroless-${ENVOY_VERSION}
COPY --from=build /build/libtestdynamicmodule.so /usr/local/lib/libtestdynamicmodule.so
