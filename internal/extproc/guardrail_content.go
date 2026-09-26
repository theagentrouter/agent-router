// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/sjson"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

type guardrailContent struct {
	text []byte
	path string
}

var guardrailTextFields = map[string]struct{}{
	"content": {}, "input": {}, "instructions": {}, "output_text": {},
	"prompt": {}, "refusal": {}, "system": {}, "text": {},
}

var guardrailContainerFields = map[string]struct{}{
	"choices": {}, "contents": {}, "delta": {}, "message": {}, "messages": {},
	"output": {}, "parts": {},
}

func extractGuardrailContents(body []byte) ([]guardrailContent, error) {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, fmt.Errorf("cannot extract guardrail text from JSON body: %w", err)
	}
	var contents []guardrailContent
	walkGuardrailContent(value, "", false, &contents)
	return contents, nil
}

func walkGuardrailContent(value any, path string, capture bool, contents *[]guardrailContent) {
	switch typed := value.(type) {
	case string:
		if capture && typed != "" {
			*contents = append(*contents, guardrailContent{text: []byte(typed), path: path})
		}
	case []any:
		for i := range typed {
			walkGuardrailContent(typed[i], appendGuardrailPath(path, strconv.Itoa(i)), capture, contents)
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			childPath := appendGuardrailPath(path, escapeGuardrailPathKey(key))
			if _, ok := guardrailTextFields[key]; ok {
				walkGuardrailContent(typed[key], childPath, true, contents)
				continue
			}
			if _, ok := guardrailContainerFields[key]; ok {
				walkGuardrailContent(typed[key], childPath, false, contents)
			}
		}
	}
}

func appendGuardrailPath(path, component string) string {
	if path == "" {
		return component
	}
	return path + "." + component
}

func escapeGuardrailPathKey(key string) string {
	replacer := strings.NewReplacer("\\", "\\\\", ".", "\\.", "*", "\\*", "?", "\\?", "#", "\\#")
	return replacer.Replace(key)
}

func replaceGuardrailContent(body []byte, content guardrailContent, replacement []byte) ([]byte, error) {
	if content.path == "" {
		return replacement, nil
	}
	return sjson.SetBytesOptions(body, content.path, string(replacement), &sjson.Options{ReplaceInPlace: true})
}
