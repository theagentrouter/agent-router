// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package inferencepool

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestIsBackendRef(t *testing.T) {
	for _, tc := range []struct {
		name  string
		group *gwapiv1.Group
		kind  *gwapiv1.Kind
		want  bool
	}{
		{name: "InferencePool", group: ptr.To(gwapiv1.Group("inference.networking.k8s.io")), kind: ptr.To(gwapiv1.Kind("InferencePool")), want: true},
		{name: "Service", want: false},
		{name: "other group", group: ptr.To(gwapiv1.Group("inference.networking.x-k8s.io")), kind: ptr.To(gwapiv1.Kind("InferencePool")), want: false},
		{name: "other kind", group: ptr.To(gwapiv1.Group("inference.networking.k8s.io")), kind: ptr.To(gwapiv1.Kind("Backend")), want: false},
		{name: "group only", group: ptr.To(gwapiv1.Group("inference.networking.k8s.io")), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := &gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Group: tc.group, Kind: tc.kind, Name: "pool"}}
			require.Equal(t, tc.want, IsBackendRef(ref))
		})
	}
}
