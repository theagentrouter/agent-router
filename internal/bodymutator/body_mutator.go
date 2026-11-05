// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package bodymutator

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

type BodyMutator struct {
	// originalBody is the original request body for retry scenarios
	originalBody []byte

	// bodyMutations is the body mutations to apply
	bodyMutations *filterapi.HTTPBodyMutation
}

func NewBodyMutator(bodyMutations *filterapi.HTTPBodyMutation, originalBody []byte) *BodyMutator {
	return &BodyMutator{
		originalBody:  originalBody,
		bodyMutations: bodyMutations,
	}
}

// HasMutations reports whether this BodyMutator was constructed with a
// non-nil HTTPBodyMutation config. When false, Mutate is a no-op that
// returns its input unchanged, so callers can short-circuit the
// upstream filter's body-replacement path entirely.
func (b *BodyMutator) HasMutations() bool {
	return b != nil && b.bodyMutations != nil
}

// Mutate mutates the request body based on the body mutations.
func (b *BodyMutator) Mutate(requestBody []byte) ([]byte, error) {
	if b.bodyMutations == nil {
		return requestBody, nil
	}

	mutatedBody := requestBody
	var err error

	// Apply removals first
	if len(b.bodyMutations.Remove) > 0 {
		for _, fieldName := range b.bodyMutations.Remove {
			if fieldName != "" {
				// TODO optimize by using SetBytesOption to avoid underlying sjson copy.
				mutatedBody, err = sjson.DeleteBytes(mutatedBody, fieldName)
				if err != nil {
					return nil, fmt.Errorf("failed to remove field %s: %w", fieldName, err)
				}
			}
		}
	}

	// Apply sets
	if len(b.bodyMutations.Set) > 0 {
		for _, field := range b.bodyMutations.Set {
			if field.Path != "" {
				// Check value type to determine appropriate sjson method
				// TODO handle JSON value check in configuration load time too.
				if gjson.Valid(field.Value) {
					// Use SetRawBytes for JSON values (quoted strings, numbers, booleans, objects, arrays)
					mutatedBody, err = sjson.SetRawBytesOptions(mutatedBody, field.Path, []byte(field.Value), &sjson.Options{ReplaceInPlace: true})
				} else {
					// Use SetBytes for plain string values
					mutatedBody, err = sjson.SetBytesOptions(mutatedBody, field.Path, field.Value, &sjson.Options{ReplaceInPlace: true})
				}
				if err != nil {
					return nil, fmt.Errorf("failed to set field %s: %w", field.Path, err)
				}
			}
		}
	}

	return mutatedBody, nil
}
