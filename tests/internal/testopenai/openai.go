// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package testopenai

import (
	"fmt"
	"reflect"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

// extractModel extracts the Model field from an OpenAI request body using reflection.
// Works with ChatCompletionRequest, CompletionRequest, and EmbeddingRequest.
func extractModel(requestBody any) string {
	v := reflect.ValueOf(requestBody)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	modelField := v.FieldByName("Model")
	if !modelField.IsValid() {
		return ""
	}
	return fmt.Sprint(modelField.Interface())
}

// extractModelFromBody extracts the model field from a JSON request body.
func extractModelFromBody(body string) string {
	var reqBody map[string]any
	if err := json.Unmarshal([]byte(body), &reqBody); err != nil {
		return ""
	}
	model, _ := reqBody["model"].(string)
	return model
}
