// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStreamOverloadedError(t *testing.T) {
	cause := errors.New("upstream overload")
	err := &StreamOverloadedError{Err: cause}
	require.EqualError(t, err, "upstream overloaded mid-stream: upstream overload")
	require.ErrorIs(t, err, cause)

	require.EqualError(t, &StreamOverloadedError{}, "upstream overloaded mid-stream")
}

func TestStreamResponseError(t *testing.T) {
	cause := errors.New("api_error")
	err := &StreamResponseError{Err: cause}
	require.ErrorIs(t, err, cause)
	require.EqualError(t, err, "upstream failed mid-stream: api_error")
	require.EqualError(t, &StreamResponseError{}, "upstream failed mid-stream")
}
