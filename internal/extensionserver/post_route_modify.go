// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"context"
	"fmt"
	"strings"

	egextension "github.com/envoyproxy/gateway/proto/extension"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// PostRouteModify allows an extension to modify routes after they are generated.
func (s *Server) PostRouteModify(_ context.Context, req *egextension.PostRouteModifyRequest) (*egextension.PostRouteModifyResponse, error) {
	if req.Route == nil {
		return nil, nil
	}

	// Check if we have backend extension resources (InferencePool resources).
	if req.PostRouteContext == nil || len(req.PostRouteContext.ExtensionResources) == 0 {
		// No backend extension resources, skip.
		return &egextension.PostRouteModifyResponse{Route: req.Route}, nil
	}

	// Parse InferencePool resources from BackendExtensionResources.
	inferencePools := s.constructInferencePoolsFrom(req.PostRouteContext.ExtensionResources)

	// If we found InferencePools, configure the route with the ext_proc per-route config.
	// InferencePool configuration only applies to forwarding routes (RouteAction).
	// Non-forwarding routes (e.g. DirectResponse, Redirect) cannot route to an InferencePool.
	if inferencePools != nil {
		routeAction := req.Route.GetRoute()
		if routeAction == nil {
			names := make([]string, len(inferencePools))
			for i, pool := range inferencePools {
				names[i] = fmt.Sprintf("%s/%s", pool.Namespace, pool.Name)
			}
			return nil, status.Errorf(codes.FailedPrecondition, "cannot configure InferencePool %s on non-forwarding route %q", strings.Join(names, ","), req.Route.Name)
		}

		for _, pool := range inferencePools {
			if pool.Spec.EndpointPickerRef == nil {
				// No endpoint picker configured for this InferencePool (spec.endpointPickerRef is
				// optional as of Gateway API Inference Extension v1.5.0). We don't yet support
				// routing traffic without one, so leave the route unmodified rather than wiring up
				// EPP config that would panic on the nil reference.
				return &egextension.PostRouteModifyResponse{Route: req.Route}, nil
			}
		}

		// Disable auto host rewrite to prevent Envoy from overriding the host header
		// set by the endpoint picker. The endpoint picker sets the destination via
		// x-gateway-destination-endpoint header and we need to preserve the original
		// host for proper routing to the selected endpoint.
		routeAction.HostRewriteSpecifier = &routev3.RouteAction_AutoHostRewrite{
			AutoHostRewrite: wrapperspb.Bool(false),
		}
		if req.Route.TypedPerFilterConfig == nil {
			req.Route.TypedPerFilterConfig = make(map[string]*anypb.Any)
		}
		buildEPPMetadataForRoute(req.Route, inferencePools)
	}

	return &egextension.PostRouteModifyResponse{Route: req.Route}, nil
}
