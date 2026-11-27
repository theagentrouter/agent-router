// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		paths  []string
		expErr string
		expOut string
	}{
		{
			name:   "valid basic config",
			paths:  []string{"testdata/translate_basic.in.yaml"},
			expOut: "OK: config is valid\n",
		},
		{
			name:   "invalid parentRef kind",
			paths:  []string{"testdata/validate_invalid_parent_ref.yaml"},
			expErr: "config validation failed",
		},
		{
			name:   "file not found",
			paths:  []string{"testdata/does_not_exist.yaml"},
			expErr: "error reading file",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := validate(context.Background(), &cmdValidate{Paths: tc.paths}, &stdout, &stderr)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expOut, stdout.String())
			}
		})
	}
}
