// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
)

func TestStreamOverloadedError(t *testing.T) {
	cause := errors.New("upstream overload")
	err := &StreamOverloadedError{Err: cause}
	require.EqualError(t, err, "upstream overloaded mid-stream: upstream overload")
	require.ErrorIs(t, err, cause)

	require.EqualError(t, &StreamOverloadedError{}, "upstream overloaded mid-stream")
}

func TestStreamingThinkingBlockProvider(t *testing.T) {
	bedrockDelta := func(payload string) []byte {
		t.Helper()
		var buf bytes.Buffer
		err := eventstream.NewEncoder().Encode(&buf, eventstream.Message{
			Headers: eventstream.Headers{{Name: ":event-type", Value: eventstream.StringValue("contentBlockDelta")}},
			Payload: []byte(payload),
		})
		require.NoError(t, err)
		return buf.Bytes()
	}

	tests := []struct {
		provider       string
		translator     OpenAIChatCompletionTranslator
		reasoningEvent []byte
		signatureEvent []byte
		wantSignature  string
	}{
		{
			provider:       "gemini",
			translator:     &openAIToGCPVertexAITranslatorV1ChatCompletion{stream: true},
			reasoningEvent: []byte(`data: {"candidates":[{"content":{"parts":[{"thought":true,"text":"Synthetic thought."}]}}]}` + "\n\n"),
			signatureEvent: []byte(`data: {"candidates":[{"content":{"parts":[{"text":"Hello!","thoughtSignature":"AQIDBAUGBwg="}]}}]}` + "\n\n"),
			wantSignature:  "AQIDBAUGBwg=",
		},
		{
			provider:       "bedrock",
			translator:     &openAIToAWSBedrockTranslatorV1ChatCompletion{stream: true},
			reasoningEvent: bedrockDelta(`{"contentBlockIndex":0,"delta":{"reasoningContent":{"text":"Synthetic thought."}}}`),
			signatureEvent: bedrockDelta(`{"contentBlockIndex":0,"delta":{"reasoningContent":{"signature":"ERITFBUWFxg="}}}`),
			wantSignature:  "ERITFBUWFxg=",
		},
	}
	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			translate := func(event []byte) *openai.ChatCompletionResponseChunkChoiceDelta {
				t.Helper()
				_, body, _, _, err := tc.translator.ResponseBody(nil, bytes.NewReader(event), false, nil)
				require.NoError(t, err)
				chunks := getChatCompletionResponseChunk(body)
				require.Len(t, chunks, 1)
				require.Len(t, chunks[0].Choices, 1)
				delta := chunks[0].Choices[0].Delta
				require.NotNil(t, delta)
				return delta
			}

			reasoning := translate(tc.reasoningEvent)
			require.NotNil(t, reasoning.ReasoningContent)
			require.Equal(t, "Synthetic thought.", reasoning.ReasoningContent.Text)
			require.Empty(t, reasoning.ThinkingBlocks)

			signed := translate(tc.signatureEvent)
			require.Nil(t, signed.ReasoningContent)
			require.Len(t, signed.ThinkingBlocks, 1)
			block := signed.ThinkingBlocks[0]
			require.Equal(t, "thinking", block.Type)
			require.Equal(t, tc.wantSignature, block.Signature)
			require.Equal(t, tc.provider, block.Provider)
		})
	}
}
