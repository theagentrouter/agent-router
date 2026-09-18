// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"fmt"
	"strconv"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

// unmarshalJSONNestedUnion is tuned to be faster with substantially reduced
// allocations vs openai-go which has heavy use of reflection.
// This is used by completion API prompts and only supports: string, []string, []int64, [][]int64
func unmarshalJSONNestedUnion(typ string, data []byte) (any, error) {
	idx, err := skipLeadingWhitespace(typ, data, 0)
	if err != nil {
		return nil, err
	}

	switch data[idx] {
	case '"':
		return unquoteOrUnmarshalJSONString(typ, data)

	case '[':
		// Array: skip to first element
		idx++
		if idx, err = skipLeadingWhitespace(typ, data, idx); err != nil {
			return nil, err
		}

		// Empty array - default to string array
		if data[idx] == ']' {
			return []string{}, nil
		}

		// Determine element type
		switch data[idx] {
		case '"':
			// []string
			var strs []string
			if err := json.Unmarshal(data, &strs); err != nil {
				return nil, fmt.Errorf("cannot unmarshal %s as []string: %w", typ, err)
			}
			return strs, nil

		case '[':
			// [][]int64
			var intArrays [][]int64
			if err := json.Unmarshal(data, &intArrays); err != nil {
				return nil, fmt.Errorf("cannot unmarshal %s as [][]int64: %w", typ, err)
			}
			return intArrays, nil

		case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			return unmarshalJSONInt64s(typ, data)
		default:
			return nil, fmt.Errorf("invalid %s array element", typ)
		}

	default:
		return nil, fmt.Errorf("invalid %s type (must be string or array)", typ)
	}
}

// unmarshalJSONEmbeddingInput is specialized for embedding API input parsing.
// It supports: string, []string, EmbeddingInputItem, []EmbeddingInputItem, []int64, [][]int64
func unmarshalJSONEmbeddingInput(typ string, data []byte) (any, error) {
	idx, err := skipLeadingWhitespace(typ, data, 0)
	if err != nil {
		return nil, err
	}

	switch data[idx] {
	case '"':
		return unquoteOrUnmarshalJSONString(typ, data)

	case '{':
		// Single object with content/task_type/title
		var item EmbeddingInputItem
		err = json.Unmarshal(data, &item)
		if err != nil {
			return nil, fmt.Errorf("cannot unmarshal %s as EmbeddingInputItem: %w", typ, err)
		}
		// Validate that the content field is not empty
		if item.Content.IsEmpty() {
			return nil, fmt.Errorf("invalid %s type (must be string, object, or array)", typ)
		}
		return item, nil

	case '[':
		// Array: skip to first element
		idx++
		if idx, err = skipLeadingWhitespace(typ, data, idx); err != nil {
			return nil, err
		}

		// Empty array - default to string array
		if data[idx] == ']' {
			return []string{}, nil
		}

		// Determine element type
		switch data[idx] {
		case '"':
			// []string
			var strs []string
			if err := json.Unmarshal(data, &strs); err != nil {
				return nil, fmt.Errorf("cannot unmarshal %s as []string: %w", typ, err)
			}
			return strs, nil

		case '{':
			// []EmbeddingInputItem
			var items []EmbeddingInputItem
			if err := json.Unmarshal(data, &items); err != nil {
				return nil, fmt.Errorf("cannot unmarshal %s as []EmbeddingInputItem: %w", typ, err)
			}
			// Validate that all items have non-empty content
			for _, item := range items {
				if item.Content.IsEmpty() {
					return nil, fmt.Errorf("invalid %s array element", typ)
				}
			}
			return items, nil

		case '[':
			// [][]int64
			var intArrays [][]int64
			if err := json.Unmarshal(data, &intArrays); err != nil {
				return nil, fmt.Errorf("cannot unmarshal %s as [][]int64: %w", typ, err)
			}
			return intArrays, nil

		case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			return unmarshalJSONInt64s(typ, data)
		default:
			return nil, fmt.Errorf("invalid %s array element", typ)
		}

	default:
		return nil, fmt.Errorf("invalid %s type (must be string, object, or array)", typ)
	}
}

// skipLeadingWhitespace is unlikely to return anything except zero, but this
// allows us to use strconv.Unquote for the fast path.
func skipLeadingWhitespace(typ string, data []byte, idx int) (int, error) {
	for idx < len(data) && (data[idx] == ' ' || data[idx] == '\t' || data[idx] == '\n' || data[idx] == '\r') {
		idx++
	}
	if idx >= len(data) {
		return 0, fmt.Errorf("truncated %s data", typ)
	}
	return idx, nil
}

func unmarshalJSONInt64s(typ string, data []byte) ([]int64, error) {
	var ints []int64
	if err := json.Unmarshal(data, &ints); err != nil {
		return nil, fmt.Errorf("cannot unmarshal %s as []int64: %w", typ, err)
	}
	return ints, nil
}

func unquoteOrUnmarshalJSONString(typ string, data []byte) (string, error) {
	// Fast-path parse normal quoted string.
	s, err := strconv.Unquote(string(data))
	if err == nil {
		return s, nil
	}

	// In rare case of escaped forward slash `\/`, strconv.Unquote will fail.
	// We don't double-check first because it implies scanning the whole string
	// for an edge case which is unlikely as most serialization is in python
	// and json.dumps() does not escape forward slashes (/) in string values.
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return "", fmt.Errorf("cannot unmarshal %s as string: %w", typ, err)
	}
	return str, nil
}
