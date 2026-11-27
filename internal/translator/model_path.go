// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"fmt"
	"net/url"

	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// modelPathSegment escapes model as one path segment. Empty and dot segments survive escaping and move under path normalization.
func modelPathSegment(model string) (string, error) {
	switch model {
	case "", ".", "..":
		return "", fmt.Errorf("%w: model must not be empty, '.' or '..'", internalapi.ErrInvalidRequestBody)
	}
	return url.PathEscape(model), nil
}
