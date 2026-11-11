// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

func NewResponsesOpenAIToChatCompletionTranslator(prefix string, modelNameOverride internalapi.ModelNameOverride) OpenAIResponsesTranslator {
	return NewResponsesViaChatCompletionTranslator(
		NewChatCompletionOpenAIToOpenAITranslator(prefix, modelNameOverride),
	)
}
