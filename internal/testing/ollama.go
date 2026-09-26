// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package internaltesting

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// OllamaModel represents the different model types defined in .env.ollama.
type OllamaModel int

const (
	ChatModel OllamaModel = iota
	ThinkingModel
	CompletionModel
	EmbeddingsModel
)

// String returns the environment variable name for this model type.
func (m OllamaModel) String() string {
	switch m {
	case ChatModel:
		return "CHAT_MODEL"
	case ThinkingModel:
		return "THINKING_MODEL"
	case CompletionModel:
		return "COMPLETION_MODEL"
	case EmbeddingsModel:
		return "EMBEDDINGS_MODEL"
	default:
		return ""
	}
}

// GetOllamaModel reads the specified model from .env.ollama relative to the project root.
// Returns an error if the file is missing or the model is not found.
func GetOllamaModel(model OllamaModel) (string, error) {
	b, err := os.ReadFile(filepath.Join(FindProjectRoot(), ".env.ollama"))
	if err != nil {
		return "", err
	}
	prefix := model.String() + "="
	for line := range strings.SplitSeq(string(b), "\n") {
		if after, ok := strings.CutPrefix(line, prefix); ok {
			return after, nil
		}
	}
	return "", fmt.Errorf("%s not found in .env.ollama", model.String())
}

// CheckIfOllamaReady verifies if Ollama server is ready and the model is available.
// Returns an error if Ollama is not reachable or the model is not available.
func CheckIfOllamaReady(modelName string) error {
	req, err := http.NewRequest(http.MethodGet, "http://localhost:11434/api/tags", nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to connect to Ollama: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}
	if !strings.Contains(string(body), modelName) {
		return fmt.Errorf("model %q not found in ollama", modelName)
	}
	return nil
}
