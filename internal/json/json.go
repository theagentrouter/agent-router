// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package json // nolint: revive

import (
	"testing"

	sonicjson "github.com/bytedance/sonic" // nolint: depguard
)

var (
	config = sonicjson.Config{
		CaseSensitive: true,
	}.Froze()

	// Unmarshal is equivalent to encoding/json.Unmarshal.
	Unmarshal = config.Unmarshal
	// Marshal is equivalent to encoding/json.Marshal.
	Marshal = config.Marshal
	// NewEncoder is equivalent to encoding/json.NewEncoder.
	NewEncoder = config.NewEncoder
	// NewDecoder is equivalent to encoding/json.NewDecoder.
	NewDecoder = config.NewDecoder
	// MarshalForDeterministicTesting marshals a value to JSON in a deterministic way for testing.
	// The normal sonic configuration does not guarantee deterministic output in terms of field order.
	// It panics if called outside of tests.
	MarshalForDeterministicTesting = func(v any) ([]byte, error) {
		if !testing.Testing() {
			panic("MarshalForDeterministicTesting can only be called from tests")
		}
		return sonicjson.ConfigStd.Marshal(v)
	}
)

type (
	// RawMessage is equivalent to encoding/json.RawMessage.
	RawMessage = sonicjson.NoCopyRawMessage
	// Marshaler is the function signature of encoding/json.Marshal.
	Marshaler = func(any) ([]byte, error)
)
