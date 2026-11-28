// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openinference

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStringView(t *testing.T) {
	require.Empty(t, StringView(nil))
	require.Empty(t, StringView([]byte{}))
	b := []byte("hello")
	s := StringView(b)
	require.Equal(t, "hello", s)
	// StringView aliases the slice: an in-place edit is visible through s.
	b[0] = 'H'
	require.Equal(t, "Hello", s)
}
