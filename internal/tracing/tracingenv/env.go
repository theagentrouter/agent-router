// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package tracingenv provides environment variable parsing helpers shared by
// the tracing semantic convention implementations.
package tracingenv

import (
	"os"
	"strconv"
)

// getEnv reads a value from an environment variable and parses it using the provided parser.
// Returns defaultValue if the variable is not set or cannot be parsed.
func getEnv[T any](key string, defaultValue T, parse func(string) (T, error)) T {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	parsed, err := parse(value)
	if err != nil {
		return defaultValue
	}
	return parsed
}

// GetBoolEnv reads a boolean value from an environment variable.
// Returns defaultValue if the variable is not set or cannot be parsed.
func GetBoolEnv(key string, defaultValue bool) bool {
	return getEnv(key, defaultValue, strconv.ParseBool)
}

// GetIntEnv reads an integer value from an environment variable.
// Returns defaultValue if the variable is not set or cannot be parsed.
func GetIntEnv(key string, defaultValue int) int {
	return getEnv(key, defaultValue, strconv.Atoi)
}
