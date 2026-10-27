// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracingenv

import (
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetEnv(t *testing.T) {
	parse := func(s string) (string, error) {
		if s == "boom" {
			return "", errors.New("unparseable")
		}
		return "parsed:" + s, nil
	}

	tests := []struct {
		name     string
		set      bool
		value    string
		expected string
	}{
		{name: "unset returns default", set: false, expected: "fallback"},
		{name: "empty returns default", set: true, value: "", expected: "fallback"},
		{name: "parses value", set: true, value: "ok", expected: "parsed:ok"},
		{name: "parse error returns default", set: true, value: "boom", expected: "fallback"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const key = "AI_GATEWAY_TEST_GET_ENV"
			if tc.set {
				t.Setenv(key, tc.value)
			}
			require.Equal(t, tc.expected, getEnv(key, "fallback", parse))
		})
	}
}

func TestGetBoolEnv(t *testing.T) {
	tests := []struct {
		name     string
		set      bool
		value    string
		def      bool
		expected bool
	}{
		{name: "unset returns default true", set: false, def: true, expected: true},
		{name: "unset returns default false", set: false, def: false, expected: false},
		{name: "true", set: true, value: "true", def: false, expected: true},
		{name: "false", set: true, value: "false", def: true, expected: false},
		{name: "1 parses as true", set: true, value: "1", def: false, expected: true},
		{name: "0 parses as false", set: true, value: "0", def: true, expected: false},
		{name: "TRUE parses as true", set: true, value: "TRUE", def: false, expected: true},
		// A typo must not silently flip a privacy-relevant flag: it keeps the default.
		{name: "typo returns default", set: true, value: "yess", def: false, expected: false},
		{name: "typo returns default true", set: true, value: "yess", def: true, expected: true},
		{name: "empty returns default", set: true, value: "", def: true, expected: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const key = "AI_GATEWAY_TEST_GET_BOOL_ENV"
			if tc.set {
				t.Setenv(key, tc.value)
			}
			require.Equal(t, tc.expected, GetBoolEnv(key, tc.def))
		})
	}
}

func TestGetIntEnv(t *testing.T) {
	tests := []struct {
		name     string
		set      bool
		value    string
		expected int
	}{
		{name: "unset returns default", set: false, expected: 32000},
		{name: "parses positive", set: true, value: "42", expected: 42},
		{name: "parses zero", set: true, value: "0", expected: 0},
		{name: "parses negative", set: true, value: "-1", expected: -1},
		{name: "non-numeric returns default", set: true, value: "abc", expected: 32000},
		{name: "float returns default", set: true, value: "1.5", expected: 32000},
		{name: "empty returns default", set: true, value: "", expected: 32000},
		{name: "max int", set: true, value: strconv.Itoa(maxInt), expected: maxInt},
		{name: "overflow returns default", set: true, value: "9223372036854775808", expected: 32000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const key = "AI_GATEWAY_TEST_GET_INT_ENV"
			if tc.set {
				t.Setenv(key, tc.value)
			}
			require.Equal(t, tc.expected, GetIntEnv(key, 32000))
		})
	}
}

const maxInt = int(^uint(0) >> 1)
