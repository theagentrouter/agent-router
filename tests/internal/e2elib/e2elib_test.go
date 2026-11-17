// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2elib

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayPodsHaveExtProc(t *testing.T) {
	const withSidecar = "envoy-a\t\tai-gateway-extproc envoy shutdown-manager\n"
	const withoutSidecar = "envoy-b\t\t envoy shutdown-manager\n"
	const terminating = "envoy-c\t2026-11-12T02:53:59Z\tai-gateway-extproc envoy shutdown-manager\n"
	const lookalike = "envoy-d\t\t not-ai-gateway-extproc envoy shutdown-manager\n"

	for _, tc := range []struct {
		name   string
		output string
		errMsg string
	}{
		{name: "sidecar present", output: withSidecar},
		{name: "two pods with sidecars", output: withSidecar + "envoy-e\t\tai-gateway-extproc envoy\n"},
		{name: "no pods", output: "", errMsg: "no gateway pod found"},
		{name: "first pod has no sidecar", output: withoutSidecar, errMsg: "pod envoy-b has no ai-gateway-extproc container"},
		{name: "old pod still serving during rollout", output: withSidecar + withoutSidecar, errMsg: "pod envoy-b has no"},
		{name: "old pod terminating", output: withSidecar + terminating, errMsg: "pod envoy-c is terminating"},
		{name: "container name is a superstring", output: lookalike, errMsg: "pod envoy-d has no"},
		{name: "malformed listing", output: "garbage\n", errMsg: "unexpected pod listing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := gatewayPodsHaveExtProc(tc.output)
			if tc.errMsg == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}
