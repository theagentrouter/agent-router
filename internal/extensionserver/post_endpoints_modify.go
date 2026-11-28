// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"context"
	"fmt"

	egextension "github.com/envoyproxy/gateway/proto/extension"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

// PostEndpointsModify is called by Envoy Gateway for the ClusterLoadAssignment of every EDS cluster.
//
// AIServiceBackends always reference Envoy Gateway Backends (enforced by CRD validation). Envoy
// Gateway turns Backends with static endpoints (`ip` or `unix`) into EDS clusters, whose endpoints are
// not inlined in the Cluster that PostTranslateModify sees; `fqdn` endpoints are inlined. When an
// AIGatewayRoute rule has several backendRefs, their endpoints share one such cluster, so the
// backend each endpoint belongs to can only be recorded here, on the endpoints themselves. The
// upstream ext_proc filter reads it to pick the backend's configuration, including its credentials.
//
// This hook only runs when the "Endpoints" xDS translator hook is registered in the Envoy Gateway
// extensionManager configuration. aigw run (standalone mode) always registers it
// (cmd/aigw/envoy-gateway-config.yaml). On Kubernetes it is NOT registered by default
// (manifests/envoy-gateway-values.yaml) and users must enable it explicitly in their Envoy Gateway
// Helm values. Envoy Gateway calls it for every EDS cluster on every translation, with no filter
// on extension resources, so on large Gateways it adds a gRPC call per cluster. Without it, rules
// with several IP-endpoint backends fail closed (see maybeModifyCluster).
//
// An empty response tells Envoy Gateway to keep its LoadAssignment unchanged, so the
// LoadAssignment is only sent back when it was modified.
func (s *Server) PostEndpointsModify(ctx context.Context, req *egextension.PostEndpointsModifyRequest) (*egextension.PostEndpointsModifyResponse, error) {
	cla := req.GetLoadAssignment()
	resp := &egextension.PostEndpointsModifyResponse{}
	if cla == nil {
		return resp, nil
	}

	clusterName, err := parseAIGatewayClusterName(cla.ClusterName)
	if err != nil {
		return resp, nil
	}
	var aigwRoute aigv1b1.AIGatewayRoute
	if err = s.k8sClient.Get(ctx, client.ObjectKey{Namespace: clusterName.namespace, Name: clusterName.routeName}, &aigwRoute); err != nil {
		if apierrors.IsNotFound(err) {
			return resp, nil
		}
		s.log.Error(err, "failed to get AIGatewayRoute object",
			"namespace", clusterName.namespace, "name", clusterName.routeName)
		return nil, fmt.Errorf("failed to get AIGatewayRoute %s/%s: %w", clusterName.namespace, clusterName.routeName, err)
	}
	if clusterName.ruleIndex >= len(aigwRoute.Spec.Rules) {
		s.log.Info("HTTPRoute rule index out of range", "cluster_name", cla.ClusterName, "rule_index", clusterName.ruleIndex)
		return resp, nil
	}
	rule := &aigwRoute.Spec.Rules[clusterName.ruleIndex]
	if clusterName.backendRefIndex != noBackendRefIndex && clusterName.backendRefIndex >= len(rule.BackendRefs) {
		s.log.Info("HTTPRoute backend index out of range", "cluster_name", cla.ClusterName, "backend_index", clusterName.backendRefIndex)
		return resp, nil
	}
	// InferencePool clusters are ORIGINAL_DST clusters configured by PostClusterModify.
	for i := range rule.BackendRefs {
		if rule.BackendRefs[i].IsInferencePool() {
			return resp, nil
		}
	}

	if err = s.stampLoadAssignment(ctx, cla.ClusterName, cla, &aigwRoute, clusterName.ruleIndex, clusterName.backendRefIndex); err != nil {
		return nil, err
	}
	return &egextension.PostEndpointsModifyResponse{LoadAssignment: cla}, nil
}
