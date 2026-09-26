// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"errors"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

// defaultExtensionServerCheckInterval is how often [extensionServerCheck] re-checks the wiring.
const defaultExtensionServerCheckInterval = time.Minute

// errExtensionServerNotInvoked is reported when Envoy Gateway never calls the extension server.
var errExtensionServerNotInvoked = errors.New("the AI Gateway extension server has never been invoked by Envoy Gateway")

// extensionServerCheck is a [manager.Runnable] that makes a missing Envoy Gateway extension server
// wiring visible.
//
// The external processor is injected into the Envoy pod by the mutating webhook, but the ext_proc HTTP
// filter that actually sends traffic through it is only added from the extension server hook
// PostTranslateModify. When Envoy Gateway is installed without the extensionManager configuration that
// AI Gateway requires, or cannot reach the extension server at all, that hook is never invoked: the
// external processor comes up healthy, every AI Gateway resource is reported as Accepted, and requests
// silently reach the upstream without credential injection or model extraction.
//
// Note that the check observes the extension server running in this process only. Envoy Gateway keeps a
// single connection to the extension server service, so with more than one controller replica the
// replicas Envoy Gateway is not connected to also report the wiring as missing.
type extensionServerCheck struct {
	client   client.Client
	logger   logr.Logger
	interval time.Duration
	// invoked reports whether Envoy Gateway has invoked the extension server of this process.
	invoked func() bool
}

// Start implements [manager.Runnable]. It blocks until the wiring is confirmed or ctx is done.
func (e *extensionServerCheck) Start(ctx context.Context) error {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if e.check(ctx) {
				// Envoy Gateway is wired to the extension server, so there is nothing left to watch.
				return nil
			}
		}
	}
}

// check returns true once Envoy Gateway has invoked the extension server. Until then it logs an
// actionable error whenever there is at least one route that would need the AI Gateway filters.
func (e *extensionServerCheck) check(ctx context.Context) bool {
	if e.invoked() {
		return true
	}

	var aiGatewayRoutes aigv1b1.AIGatewayRouteList
	if err := e.client.List(ctx, &aiGatewayRoutes); err != nil {
		e.logger.Error(err, "failed to list AIGatewayRoutes")
		return false
	}
	var mcpRoutes aigv1b1.MCPRouteList
	if err := e.client.List(ctx, &mcpRoutes); err != nil {
		e.logger.Error(err, "failed to list MCPRoutes")
		return false
	}
	if len(aiGatewayRoutes.Items) == 0 && len(mcpRoutes.Items) == 0 {
		// Without any route there is nothing for Envoy Gateway to translate yet.
		return false
	}

	e.logger.Error(errExtensionServerNotInvoked,
		"the AI Gateway external processor is not attached to any Envoy filter chain, so requests bypass "+
			"credential injection and model extraction. Install Envoy Gateway with the extensionManager "+
			"configuration required by AI Gateway (manifests/envoy-gateway-values.yaml) and make sure its "+
			"service hostname and port point at the AI Gateway controller",
		"aigatewayroute_count", len(aiGatewayRoutes.Items), "mcproute_count", len(mcpRoutes.Items),
	)
	return false
}
