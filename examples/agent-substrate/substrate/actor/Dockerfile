# Copyright Envoy AI Gateway Authors
# SPDX-License-Identifier: Apache-2.0
# The full text of the Apache license is available in the LICENSE file at
# the root of the repo.

# Build from examples/agent-substrate: docker build -f substrate/actor/Dockerfile .
FROM golang:1.27.3 AS build
WORKDIR /src
# Standard library only: no modules to download.
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./substrate/actor

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]
