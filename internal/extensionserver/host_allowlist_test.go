// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"errors"
	"testing"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestWrapClusterLBInHostAllowlist(t *testing.T) {
	t.Run("CLUSTER_PROVIDED cluster is left untouched", func(t *testing.T) {
		// CLUSTER_PROVIDED clusters (e.g. InferencePool) delegate host selection and carry no
		// endpoint metadata to match against, so wrapping is skipped entirely.
		c := &clusterv3.Cluster{Name: "ip-cluster", LbPolicy: clusterv3.Cluster_CLUSTER_PROVIDED}
		require.NoError(t, wrapClusterLBInHostAllowlist(c))
		require.Nil(t, c.LoadBalancingPolicy)
	})

	t.Run("already wrapped cluster is idempotent", func(t *testing.T) {
		existing := &clusterv3.LoadBalancingPolicy{
			Policies: []*clusterv3.LoadBalancingPolicy_Policy{{
				TypedExtensionConfig: &corev3.TypedExtensionConfig{Name: hostAllowlistPolicyName},
			}},
		}
		c := &clusterv3.Cluster{Name: "c", LoadBalancingPolicy: existing}
		require.NoError(t, wrapClusterLBInHostAllowlist(c))
		// Unchanged: the existing policy list is kept as-is (no double wrap).
		require.Same(t, existing, c.LoadBalancingPolicy)
		require.Len(t, c.LoadBalancingPolicy.Policies, 1)
	})

	t.Run("nil child policy gets a least_request fallback sibling", func(t *testing.T) {
		c := &clusterv3.Cluster{Name: "c"}
		require.NoError(t, wrapClusterLBInHostAllowlist(c))
		policies := c.GetLoadBalancingPolicy().GetPolicies()
		require.Len(t, policies, 2)
		require.Equal(t, hostAllowlistPolicyName, policies[0].GetTypedExtensionConfig().GetName())
		require.Equal(t, leastRequestPolicyName, policies[1].GetTypedExtensionConfig().GetName())
	})

	t.Run("existing picker is preserved as fallback sibling", func(t *testing.T) {
		const roundRobin = "envoy.load_balancing_policies.round_robin"
		c := &clusterv3.Cluster{
			Name: "c",
			LoadBalancingPolicy: &clusterv3.LoadBalancingPolicy{
				Policies: []*clusterv3.LoadBalancingPolicy_Policy{{
					TypedExtensionConfig: &corev3.TypedExtensionConfig{Name: roundRobin},
				}},
			},
		}
		require.NoError(t, wrapClusterLBInHostAllowlist(c))
		policies := c.GetLoadBalancingPolicy().GetPolicies()
		require.Len(t, policies, 2)
		require.Equal(t, hostAllowlistPolicyName, policies[0].GetTypedExtensionConfig().GetName())
		// The original picker survives as the fallback for data planes without host_allowlist.
		require.Equal(t, roundRobin, policies[1].GetTypedExtensionConfig().GetName())
	})
}

func TestWrapClusterLBInHostAllowlistMarshalErrors(t *testing.T) {
	originalMarshalAny := marshalAny
	t.Cleanup(func() {
		marshalAny = originalMarshalAny
	})

	t.Run("least_request marshal error", func(t *testing.T) {
		marshalAny = func(proto.Message) (*anypb.Any, error) {
			return nil, errors.New("forced marshal error")
		}

		err := wrapClusterLBInHostAllowlist(&clusterv3.Cluster{Name: "c"})
		require.EqualError(t, err, "failed to marshal default least_request policy: forced marshal error")
	})

	t.Run("host_allowlist marshal error", func(t *testing.T) {
		marshalAny = func(proto.Message) (*anypb.Any, error) {
			return nil, errors.New("forced marshal error")
		}

		child := &clusterv3.LoadBalancingPolicy{
			Policies: []*clusterv3.LoadBalancingPolicy_Policy{{
				TypedExtensionConfig: &corev3.TypedExtensionConfig{Name: "existing-picker"},
			}},
		}
		err := wrapClusterLBInHostAllowlist(&clusterv3.Cluster{
			Name:                "c",
			LoadBalancingPolicy: child,
		})
		require.EqualError(t, err, "failed to marshal HostAllowlist policy: forced marshal error")
	})
}
