// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
)

func TestOpenAIToGCPVertexAITranslatorV1Embedding_RequestBody(t *testing.T) {
	tests := []struct {
		name               string
		modelNameOverride  internalapi.ModelNameOverride
		input              openai.EmbeddingRequest
		onRetry            bool
		wantError          bool
		wantPath           string
		wantBodyContains   []string // Substrings that should be present in the request body
		wantBodyNotContain []string // Substrings that must NOT be present in the request body
	}{
		{
			name: "embedding request with string input",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004"},
					Input: openai.EmbeddingRequestInput{
						Value: "This is a test text for embedding",
					},
				},
			},
			wantPath: "publishers/google/models/text-embedding-004:predict",
			wantBodyContains: []string{
				`"instances"`,
				`"content":"This is a test text for embedding"`,
				`"parameters"`,
			},
			wantBodyNotContain: []string{`"parts"`},
		},
		{
			name:              "embedding request with model override",
			modelNameOverride: "custom-embedding-model",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004"},
					Input: openai.EmbeddingRequestInput{
						Value: "Test text",
					},
				},
			},
			wantPath: "publishers/google/models/custom-embedding-model:predict",
			wantBodyContains: []string{
				`"instances"`,
				`"content":"Test text"`,
				`"parameters"`,
			},
		},
		{
			name: "embedding request with EmbeddingInputItem and task_type",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004"},
					Input: openai.EmbeddingRequestInput{
						Value: openai.EmbeddingInputItem{
							Content:  openai.EmbeddingContent{Value: "This is a document for retrieval"},
							TaskType: "RETRIEVAL_DOCUMENT",
							Title:    "Document Title",
						},
					},
				},
			},
			wantPath: "publishers/google/models/text-embedding-004:predict",
			wantBodyContains: []string{
				`"instances"`,
				`"content":"This is a document for retrieval"`,
				`"task_type":"RETRIEVAL_DOCUMENT"`,
				`"title":"Document Title"`,
				`"parameters"`,
			},
		},
		{
			name: "embedding request with array of EmbeddingInputItem",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004"},
					Input: openai.EmbeddingRequestInput{
						Value: []openai.EmbeddingInputItem{
							{
								Content:  openai.EmbeddingContent{Value: "Query about cats"},
								TaskType: "RETRIEVAL_QUERY",
							},
							{
								Content:  openai.EmbeddingContent{Value: "Document about dogs"},
								TaskType: "RETRIEVAL_DOCUMENT",
								Title:    "Dog Info",
							},
						},
					},
				},
			},
			wantPath: "publishers/google/models/text-embedding-004:predict",
			wantBodyContains: []string{
				`"instances"`,
				`"content":"Query about cats"`,
				`"task_type":"RETRIEVAL_QUERY"`,
				`"content":"Document about dogs"`,
				`"task_type":"RETRIEVAL_DOCUMENT"`,
				`"title":"Dog Info"`,
				`"parameters"`,
			},
		},
		{
			name: "embedding request with dimensions parameter",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004", Dimensions: &[]int{256}[0]},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004", Dimensions: &[]int{256}[0]},
					Input: openai.EmbeddingRequestInput{
						Value: "Text for dimension testing",
					},
				},
			},
			wantPath: "publishers/google/models/text-embedding-004:predict",
			wantBodyContains: []string{
				`"instances"`,
				`"content":"Text for dimension testing"`,
				`"parameters"`,
				`"outputDimensionality":256`,
			},
		},
		{
			name: "embedding request with SEMANTIC_SIMILARITY without title",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004"},
					Input: openai.EmbeddingRequestInput{
						Value: openai.EmbeddingInputItem{
							Content:  openai.EmbeddingContent{Value: "Text for similarity check"},
							TaskType: "SEMANTIC_SIMILARITY",
							Title:    "This title should not appear",
						},
					},
				},
			},
			wantPath: "publishers/google/models/text-embedding-004:predict",
			wantBodyContains: []string{
				`"instances"`,
				`"content":"Text for similarity check"`,
				`"task_type":"SEMANTIC_SIMILARITY"`,
				`"parameters"`,
			},
			wantBodyNotContain: []string{`"This title should not appear"`, `"title"`},
		},
		{
			name: "embedding request with auto_truncate vendor field",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004", GCPVertexAIEmbeddingVendorFields: &openai.GCPVertexAIEmbeddingVendorFields{AutoTruncate: &[]bool{true}[0]}},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004", GCPVertexAIEmbeddingVendorFields: &openai.GCPVertexAIEmbeddingVendorFields{AutoTruncate: &[]bool{true}[0]}},
					Input: openai.EmbeddingRequestInput{
						Value: "Test text for auto truncate",
					},
				},
			},
			wantPath: "publishers/google/models/text-embedding-004:predict",
			wantBodyContains: []string{
				`"instances"`,
				`"content":"Test text for auto truncate"`,
				`"parameters"`,
				`"auto_truncate":true`,
			},
		},
		{
			name: "embedding request with global task_type overriding individual task_type",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004", GCPVertexAIEmbeddingVendorFields: &openai.GCPVertexAIEmbeddingVendorFields{TaskType: "RETRIEVAL_QUERY"}},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004", GCPVertexAIEmbeddingVendorFields: &openai.GCPVertexAIEmbeddingVendorFields{TaskType: "RETRIEVAL_QUERY"}},
					Input: openai.EmbeddingRequestInput{
						Value: []openai.EmbeddingInputItem{
							{
								Content:  openai.EmbeddingContent{Value: "Query text"},
								TaskType: "RETRIEVAL_DOCUMENT", // This should be overridden
							},
							{
								Content: openai.EmbeddingContent{Value: "Another text"},
								// No task type specified
							},
						},
					},
				},
			},
			wantPath: "publishers/google/models/text-embedding-004:predict",
			wantBodyContains: []string{
				`"instances"`,
				`"content":"Query text"`,
				`"task_type":"RETRIEVAL_QUERY"`,
				`"content":"Another text"`,
				`"parameters"`,
			},
		},
		{
			name: "embedding request with array of strings",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004"},
					Input: openai.EmbeddingRequestInput{
						Value: []string{"First text", "Second text", "Third text"},
					},
				},
			},
			wantPath: "publishers/google/models/text-embedding-004:predict",
			wantBodyContains: []string{
				`"instances"`,
				`"content":"First text"`,
				`"content":"Second text"`,
				`"content":"Third text"`,
				`"parameters"`,
			},
		},
		// embedContent path tests
		{
			name: "embedContent: single string input",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview"},
					Input: openai.EmbeddingRequestInput{
						Value: "hello world",
					},
				},
			},
			wantPath: "publishers/google/models/gemini-embedding-2-preview:embedContent",
			wantBodyContains: []string{
				`"content"`,
				`"parts"`,
				`"text":"hello world"`,
			},
			wantBodyNotContain: []string{`"instances"`, `"parameters"`},
		},
		{
			name: "embedContent: multiple string inputs rejected (batch not supported)",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview"},
					Input: openai.EmbeddingRequestInput{
						Value: []string{"a", "b", "c"},
					},
				},
			},
			wantError: true,
		},
		{
			name: "embedContent: with dimensions",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview", Dimensions: &[]int{256}[0]},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview", Dimensions: &[]int{256}[0]},
					Input: openai.EmbeddingRequestInput{
						Value: "test",
					},
				},
			},
			wantPath: "publishers/google/models/gemini-embedding-2-preview:embedContent",
			wantBodyContains: []string{
				`"content"`,
				`"embedContentConfig"`,
				`"outputDimensionality":256`,
			},
			wantBodyNotContain: []string{`"instances"`, `"parameters"`},
		},
		{
			name: "embedContent: with vendor taskType (sent but may be ignored by gemini-embedding-2)",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview", GCPVertexAIEmbeddingVendorFields: &openai.GCPVertexAIEmbeddingVendorFields{TaskType: "RETRIEVAL_QUERY"}},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview", GCPVertexAIEmbeddingVendorFields: &openai.GCPVertexAIEmbeddingVendorFields{TaskType: "RETRIEVAL_QUERY"}},
					Input: openai.EmbeddingRequestInput{
						Value: "query text",
					},
				},
			},
			wantPath: "publishers/google/models/gemini-embedding-2-preview:embedContent",
			wantBodyContains: []string{
				`"content"`,
				`"embedContentConfig"`,
				`"taskType":"RETRIEVAL_QUERY"`,
			},
			wantBodyNotContain: []string{`"instances"`, `"parameters"`},
		},
		{
			name: "embedContent: EmbeddingInputItem is rejected (per-item task_type/title not supported)",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview"},
					Input: openai.EmbeddingRequestInput{
						Value: openai.EmbeddingInputItem{
							Content:  openai.EmbeddingContent{Value: "doc for retrieval"},
							TaskType: "RETRIEVAL_DOCUMENT",
							Title:    "Document Title",
						},
					},
				},
			},
			wantError: true,
		},
		{
			name: "embedContent: auto_truncate vendor field uses camelCase",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview", GCPVertexAIEmbeddingVendorFields: &openai.GCPVertexAIEmbeddingVendorFields{AutoTruncate: &[]bool{true}[0]}},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview", GCPVertexAIEmbeddingVendorFields: &openai.GCPVertexAIEmbeddingVendorFields{AutoTruncate: &[]bool{true}[0]}},
					Input: openai.EmbeddingRequestInput{
						Value: "text for auto truncate",
					},
				},
			},
			wantPath: "publishers/google/models/gemini-embedding-2-preview:embedContent",
			wantBodyContains: []string{
				`"content"`,
				`"embedContentConfig"`,
				`"autoTruncate":true`,
			},
			wantBodyNotContain: []string{`"instances"`, `"parameters"`, `"auto_truncate"`},
		},
		{
			name: "embedContent: array of EmbeddingInputItem is rejected",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview"},
					Input: openai.EmbeddingRequestInput{
						Value: []openai.EmbeddingInputItem{
							{Content: openai.EmbeddingContent{Value: "first item"}},
							{Content: openai.EmbeddingContent{Value: "second item"}},
						},
					},
				},
			},
			wantError: true,
		},
		{
			name: "gemini-embedding-001 still uses predict",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-001"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-001"},
					Input: openai.EmbeddingRequestInput{
						Value: "test",
					},
				},
			},
			wantPath: "publishers/google/models/gemini-embedding-001:predict",
			wantBodyContains: []string{
				`"instances"`,
				`"content":"test"`,
				`"parameters"`,
			},
			wantBodyNotContain: []string{`"parts"`},
		},
		{
			name:              "embedContent: model override to gemini-embedding-2-preview",
			modelNameOverride: "gemini-embedding-2-preview",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004"},
				OfCompletion: &openai.EmbeddingCompletionRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004"},
					Input: openai.EmbeddingRequestInput{
						Value: "override test",
					},
				},
			},
			wantPath: "publishers/google/models/gemini-embedding-2-preview:embedContent",
			wantBodyContains: []string{
				`"content"`,
				`"parts"`,
				`"text":"override test"`,
			},
			wantBodyNotContain: []string{`"instances"`, `"parameters"`},
		},
		// Multimodal messages path tests
		{
			name: "embedContent: single text message via messages",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview"},
				OfChat: &openai.EmbeddingChatRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview"},
					Messages: []openai.ChatCompletionMessageParamUnion{
						{OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    "user",
							Content: openai.StringOrUserRoleContentUnion{Value: "hello from messages"},
						}},
					},
				},
			},
			wantPath: "publishers/google/models/gemini-embedding-2-preview:embedContent",
			wantBodyContains: []string{
				`"content"`,
				`"parts"`,
				`"text":"hello from messages"`,
			},
			wantBodyNotContain: []string{`"instances"`, `"parameters"`},
		},
		{
			name: "embedContent: image URL message via messages",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview"},
				OfChat: &openai.EmbeddingChatRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview"},
					Messages: []openai.ChatCompletionMessageParamUnion{
						{OfUser: &openai.ChatCompletionUserMessageParam{
							Role: "user",
							Content: openai.StringOrUserRoleContentUnion{Value: []openai.ChatCompletionContentPartUserUnionParam{
								{OfImageURL: &openai.ChatCompletionContentPartImageParam{
									Type: "image_url",
									ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
										URL: "https://example.com/image.png",
									},
								}},
							}},
						}},
					},
				},
			},
			wantPath: "publishers/google/models/gemini-embedding-2-preview:embedContent",
			wantBodyContains: []string{
				`"content"`,
				`"parts"`,
				`"fileUri":"https://example.com/image.png"`,
				`"mimeType":"image/png"`,
			},
			wantBodyNotContain: []string{`"instances"`, `"parameters"`},
		},
		{
			name: "embedContent: data URI image via messages",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview"},
				OfChat: &openai.EmbeddingChatRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview"},
					Messages: []openai.ChatCompletionMessageParamUnion{
						{OfUser: &openai.ChatCompletionUserMessageParam{
							Role: "user",
							Content: openai.StringOrUserRoleContentUnion{Value: []openai.ChatCompletionContentPartUserUnionParam{
								{OfImageURL: &openai.ChatCompletionContentPartImageParam{
									Type: "image_url",
									ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
										URL: "data:image/png;base64,iVBORw0KGgo=",
									},
								}},
							}},
						}},
					},
				},
			},
			wantPath: "publishers/google/models/gemini-embedding-2-preview:embedContent",
			wantBodyContains: []string{
				`"content"`,
				`"parts"`,
				`"inlineData"`,
				`"mimeType":"image/png"`,
			},
			wantBodyNotContain: []string{`"instances"`, `"parameters"`, `"fileURI"`},
		},
		{
			name: "embedContent: mixed text + image in one user message",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview"},
				OfChat: &openai.EmbeddingChatRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview"},
					Messages: []openai.ChatCompletionMessageParamUnion{
						{OfUser: &openai.ChatCompletionUserMessageParam{
							Role: "user",
							Content: openai.StringOrUserRoleContentUnion{Value: []openai.ChatCompletionContentPartUserUnionParam{
								{OfText: &openai.ChatCompletionContentPartTextParam{
									Type: "text",
									Text: "describe this image",
								}},
								{OfImageURL: &openai.ChatCompletionContentPartImageParam{
									Type: "image_url",
									ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
										URL: "https://example.com/photo.jpg",
									},
								}},
							}},
						}},
					},
				},
			},
			wantPath: "publishers/google/models/gemini-embedding-2-preview:embedContent",
			wantBodyContains: []string{
				`"content"`,
				`"parts"`,
				`"text":"describe this image"`,
				`"fileUri":"https://example.com/photo.jpg"`,
			},
			wantBodyNotContain: []string{`"instances"`, `"parameters"`},
		},
		{
			name: "embedContent: multiple user messages accumulate parts",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview"},
				OfChat: &openai.EmbeddingChatRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview"},
					Messages: []openai.ChatCompletionMessageParamUnion{
						{OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    "user",
							Content: openai.StringOrUserRoleContentUnion{Value: "first message"},
						}},
						{OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    "user",
							Content: openai.StringOrUserRoleContentUnion{Value: "second message"},
						}},
					},
				},
			},
			wantPath: "publishers/google/models/gemini-embedding-2-preview:embedContent",
			wantBodyContains: []string{
				`"content"`,
				`"parts"`,
				`"text":"first message"`,
				`"text":"second message"`,
			},
			wantBodyNotContain: []string{`"instances"`, `"parameters"`},
		},
		{
			name: "messages with predict-path model returns error",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004"},
				OfChat: &openai.EmbeddingChatRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004"},
					Messages: []openai.ChatCompletionMessageParamUnion{
						{OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    "user",
							Content: openai.StringOrUserRoleContentUnion{Value: "test"},
						}},
					},
				},
			},
			wantError: true,
		},
		{
			name: "embedContent: messages with dimensions and taskType",
			input: openai.EmbeddingRequest{
				EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview", Dimensions: &[]int{128}[0], GCPVertexAIEmbeddingVendorFields: &openai.GCPVertexAIEmbeddingVendorFields{TaskType: "RETRIEVAL_QUERY"}},
				OfChat: &openai.EmbeddingChatRequest{
					EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview", Dimensions: &[]int{128}[0], GCPVertexAIEmbeddingVendorFields: &openai.GCPVertexAIEmbeddingVendorFields{TaskType: "RETRIEVAL_QUERY"}},
					Messages: []openai.ChatCompletionMessageParamUnion{
						{OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    "user",
							Content: openai.StringOrUserRoleContentUnion{Value: "query text"},
						}},
					},
				},
			},
			wantPath: "publishers/google/models/gemini-embedding-2-preview:embedContent",
			wantBodyContains: []string{
				`"content"`,
				`"embedContentConfig"`,
				`"parts"`,
				`"text":"query text"`,
				`"outputDimensionality":128`,
				`"taskType":"RETRIEVAL_QUERY"`,
			},
			wantBodyNotContain: []string{`"instances"`, `"parameters"`},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			translator := NewEmbeddingOpenAIToGCPVertexAITranslator("text-embedding-004", tc.modelNameOverride)
			originalBody, _ := json.Marshal(tc.input)

			headerMut, bodyMut, err := translator.RequestBody(originalBody, &tc.input, tc.onRetry)

			if tc.wantError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, headerMut)
			require.Len(t, headerMut, 2) // path and content-length headers

			// Check path header
			require.Equal(t, gcpPathHeaderName, headerMut[0].Key())
			require.Equal(t, tc.wantPath, headerMut[0].Value())

			// Check content-length header
			require.Equal(t, contentLengthHeaderName, headerMut[1].Key())

			// Check body content
			require.NotNil(t, bodyMut)
			bodyStr := string(bodyMut)
			for _, substr := range tc.wantBodyContains {
				require.Contains(t, bodyStr, substr)
			}
			for _, substr := range tc.wantBodyNotContain {
				require.NotContains(t, bodyStr, substr, "body should not contain %q", substr)
			}
		})
	}
}

func TestOpenAIToGCPVertexAITranslatorV1Embedding_ResponseHeaders(t *testing.T) {
	translator := NewEmbeddingOpenAIToGCPVertexAITranslator("text-embedding-004", "")

	headerMut, err := translator.ResponseHeaders(map[string]string{
		"content-type": "application/json",
	})

	require.NoError(t, err)
	require.Nil(t, headerMut) // No header transformations needed for embeddings
}

func TestOpenAIToGCPVertexAITranslatorV1Embedding_ResponseBody(t *testing.T) {
	tests := []struct {
		name             string
		gcpResponse      string
		wantError        bool
		wantTokenUsage   metrics.TokenUsage
		wantResponseBody openai.EmbeddingResponse
	}{
		{
			name: "successful response with embedding data",
			gcpResponse: `{
				"predictions": [
					{
						"embeddings": {
							"values": [0.1, 0.2, 0.3, 0.4, 0.5],
							"statistics": {
								"token_count": 5,
								"truncated": false
							}
						}
					}
				]
			}`,
			wantTokenUsage: tokenUsageFrom(5, -1, -1, -1, 5, -1),
			wantResponseBody: openai.EmbeddingResponse{
				Object: "list",
				Model:  "text-embedding-004",
				Data: []openai.Embedding{
					{
						Object:    "embedding",
						Index:     0,
						Embedding: openai.EmbeddingUnion{Value: []float64{0.1, 0.2, 0.3, 0.4, 0.5}},
						Truncated: false,
					},
				},
				Usage: openai.EmbeddingUsage{
					PromptTokens: 5,
					TotalTokens:  5,
				},
			},
		},
		{
			name: "response with no embedding data",
			gcpResponse: `{
				"predictions": null
			}`,
			wantTokenUsage: tokenUsageFrom(0, -1, -1, -1, 0, -1),
			wantResponseBody: openai.EmbeddingResponse{
				Object: "list",
				Model:  "text-embedding-004",
				Data:   []openai.Embedding{},
				Usage: openai.EmbeddingUsage{
					PromptTokens: 0,
					TotalTokens:  0,
				},
			},
		},
		{
			name: "response with empty embedding values",
			gcpResponse: `{
				"predictions": [
					{
						"embeddings": {
							"values": [],
							"statistics": {
								"token_count": 3,
								"truncated": false
							}
						}
					}
				]
			}`,
			wantTokenUsage: tokenUsageFrom(3, -1, -1, -1, 3, -1),
			wantResponseBody: openai.EmbeddingResponse{
				Object: "list",
				Model:  "text-embedding-004",
				Data: []openai.Embedding{
					{
						Object:    "embedding",
						Index:     0,
						Embedding: openai.EmbeddingUnion{Value: []float64{}},
						Truncated: false,
					},
				},
				Usage: openai.EmbeddingUsage{
					PromptTokens: 3,
					TotalTokens:  3,
				},
			},
		},
		{
			name: "invalid JSON response",
			gcpResponse: `{
				"embedding": invalid json
			}`,
			wantError: true,
		},
		{
			name: "response with multiple embeddings",
			gcpResponse: `{
				"predictions": [
					{
						"embeddings": {
							"values": [0.1, 0.2, 0.3],
							"statistics": {
								"token_count": 3,
								"truncated": false
							}
						}
					},
					{
						"embeddings": {
							"values": [0.4, 0.5, 0.6],
							"statistics": {
								"token_count": 4,
								"truncated": false
							}
						}
					},
					{
						"embeddings": {
							"values": [0.7, 0.8, 0.9],
							"statistics": {
								"token_count": 5,
								"truncated": true
							}
						}
					}
				]
			}`,
			wantTokenUsage: tokenUsageFrom(12, -1, -1, -1, 12, -1), // 3 + 4 + 5 = 12
			wantResponseBody: openai.EmbeddingResponse{
				Object: "list",
				Model:  "text-embedding-004",
				Data: []openai.Embedding{
					{
						Object:    "embedding",
						Index:     0,
						Embedding: openai.EmbeddingUnion{Value: []float64{0.1, 0.2, 0.3}},
						Truncated: false,
					},
					{
						Object:    "embedding",
						Index:     1,
						Embedding: openai.EmbeddingUnion{Value: []float64{0.4, 0.5, 0.6}},
						Truncated: false,
					},
					{
						Object:    "embedding",
						Index:     2,
						Embedding: openai.EmbeddingUnion{Value: []float64{0.7, 0.8, 0.9}},
						Truncated: true,
					},
				},
				Usage: openai.EmbeddingUsage{
					PromptTokens: 12,
					TotalTokens:  12,
				},
			},
		},
		{
			name: "response with truncated embedding",
			gcpResponse: `{
				"predictions": [
					{
						"embeddings": {
							"values": [0.1, 0.2],
							"statistics": {
								"token_count": 10,
								"truncated": true
							}
						}
					}
				]
			}`,
			wantTokenUsage: tokenUsageFrom(10, -1, -1, -1, 10, -1),
			wantResponseBody: openai.EmbeddingResponse{
				Object: "list",
				Model:  "text-embedding-004",
				Data: []openai.Embedding{
					{
						Object:    "embedding",
						Index:     0,
						Embedding: openai.EmbeddingUnion{Value: []float64{0.1, 0.2}},
						Truncated: true,
					},
				},
				Usage: openai.EmbeddingUsage{
					PromptTokens: 10,
					TotalTokens:  10,
				},
			},
		},
		{
			name: "response with missing statistics",
			gcpResponse: `{
				"predictions": [
					{
						"embeddings": {
							"values": [0.1, 0.2, 0.3]
						}
					}
				]
			}`,
			wantTokenUsage: tokenUsageFrom(0, -1, -1, -1, 0, -1), // No statistics provided
			wantResponseBody: openai.EmbeddingResponse{
				Object: "list",
				Model:  "text-embedding-004",
				Data: []openai.Embedding{
					{
						Object:    "embedding",
						Index:     0,
						Embedding: openai.EmbeddingUnion{Value: []float64{0.1, 0.2, 0.3}},
						Truncated: false,
					},
				},
				Usage: openai.EmbeddingUsage{
					PromptTokens: 0,
					TotalTokens:  0,
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			translator := NewEmbeddingOpenAIToGCPVertexAITranslator("text-embedding-004", "").(*openAIToGCPVertexAITranslatorV1Embedding)

			// Initialize the requestModel field (normally done in RequestBody)
			translator.requestModel = "text-embedding-004"

			headerMut, bodyMut, tokenUsage, responseModel, err := translator.ResponseBody(
				map[string]string{"content-type": "application/json"},
				strings.NewReader(tc.gcpResponse),
				true,
				nil,
			)

			if tc.wantError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, headerMut)
			require.Len(t, headerMut, 1) // content-length header
			require.Equal(t, contentLengthHeaderName, headerMut[0].Key())
			require.NotNil(t, bodyMut)
			require.Equal(t, "text-embedding-004", responseModel)

			// Check token usage
			if diff := cmp.Diff(tc.wantTokenUsage, tokenUsage, cmp.AllowUnexported(metrics.TokenUsage{})); diff != "" {
				t.Errorf("TokenUsage mismatch (-want +got):\n%s", diff)
			}

			// Parse and check response body
			var actualResponse openai.EmbeddingResponse
			err = json.Unmarshal(bodyMut, &actualResponse)
			require.NoError(t, err)

			// Check everything except the embedding values first
			require.Equal(t, tc.wantResponseBody.Object, actualResponse.Object)
			require.Equal(t, tc.wantResponseBody.Model, actualResponse.Model)
			require.Equal(t, tc.wantResponseBody.Usage, actualResponse.Usage)
			require.Len(t, actualResponse.Data, len(tc.wantResponseBody.Data))

			// For embedding values, check with tolerance due to float32->float64 conversion
			for idx := range tc.wantResponseBody.Data {
				require.Equal(t, tc.wantResponseBody.Data[idx].Object, actualResponse.Data[idx].Object)
				require.Equal(t, tc.wantResponseBody.Data[idx].Index, actualResponse.Data[idx].Index)
				require.Equal(t, tc.wantResponseBody.Data[idx].Truncated, actualResponse.Data[idx].Truncated)

				wantEmbedding := tc.wantResponseBody.Data[idx].Embedding.Value.([]float64)
				actualEmbedding := actualResponse.Data[idx].Embedding.Value.([]float64)
				require.Len(t, actualEmbedding, len(wantEmbedding))

				for i, wantVal := range wantEmbedding {
					require.InDelta(t, wantVal, actualEmbedding[i], 1e-6, "Embedding %d value at index %d", idx, i)
				}
			}
		})
	}
}

func TestOpenAIToGCPVertexAITranslatorV1Embedding_ResponseError(t *testing.T) {
	tests := []struct {
		name      string
		headers   map[string]string
		body      string
		wantError openai.Error
	}{
		{
			name: "GCP error response with structured error",
			headers: map[string]string{
				statusHeaderName: "400",
			},
			body: `{
				"error": {
					"code": 400,
					"message": "Invalid embedding request",
					"status": "INVALID_ARGUMENT"
				}
			}`,
			wantError: openai.Error{
				Type: "error",
				Error: openai.ErrorType{
					Type:    "INVALID_ARGUMENT",
					Message: "Invalid embedding request",
					Code:    &[]string{"400"}[0],
				},
			},
		},
		{
			name: "plain text error response",
			headers: map[string]string{
				statusHeaderName: "503",
			},
			body: "Service temporarily unavailable",
			wantError: openai.Error{
				Type: "error",
				Error: openai.ErrorType{
					Type:    gcpVertexAIBackendError,
					Message: "Service temporarily unavailable",
					Code:    &[]string{"503"}[0],
				},
			},
		},
		{
			name: "JSON error response without proper GCP structure",
			headers: map[string]string{
				statusHeaderName: "429",
			},
			body: `{
				"error": {
					"message": "Rate limit exceeded"
				}
			}`,
			wantError: openai.Error{
				Type: "error",
				Error: openai.ErrorType{
					Type:    "",
					Message: "Rate limit exceeded",
					Code:    &[]string{"429"}[0],
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			translator := NewEmbeddingOpenAIToGCPVertexAITranslator("text-embedding-004", "").(*openAIToGCPVertexAITranslatorV1Embedding)

			headerMut, bodyMut, err := translator.ResponseError(tc.headers, strings.NewReader(tc.body))

			require.NoError(t, err)
			require.NotNil(t, headerMut)
			require.NotNil(t, bodyMut)

			// Parse the error response
			var actualError openai.Error
			err = json.Unmarshal(bodyMut, &actualError)
			require.NoError(t, err)

			if diff := cmp.Diff(tc.wantError, actualError); diff != "" {
				t.Errorf("Error response mismatch (-want +got):\n%s", diff)
			}

			// Check that content-type and content-length headers are set
			require.Len(t, headerMut, 2)
			require.Equal(t, contentTypeHeaderName, headerMut[0].Key())
			// Use JSONEq by wrapping values as JSON strings
			require.JSONEq(t, fmt.Sprintf(`"%s"`, jsonContentType), fmt.Sprintf(`"%s"`, headerMut[0].Value()))
			require.Equal(t, contentLengthHeaderName, headerMut[1].Key())
		})
	}
}

func TestOpenAIToGCPVertexAITranslatorV1Embedding_EmbedContentResponseBody(t *testing.T) {
	tests := []struct {
		name             string
		gcpResponse      string
		wantError        bool
		wantTokenUsage   metrics.TokenUsage
		wantResponseBody openai.EmbeddingResponse
	}{
		{
			name: "embedContent response with embedding and usageMetadata",
			gcpResponse: `{
				"embedding": {"values": [0.1, 0.2, 0.3]},
				"usageMetadata": {"promptTokenCount": 3, "totalTokenCount": 3}
			}`,
			wantTokenUsage: tokenUsageFrom(3, -1, -1, -1, 3, -1),
			wantResponseBody: openai.EmbeddingResponse{
				Object: "list",
				Model:  "gemini-embedding-2-preview",
				Data: []openai.Embedding{
					{
						Object:    "embedding",
						Index:     0,
						Embedding: openai.EmbeddingUnion{Value: []float64{0.1, 0.2, 0.3}},
						Truncated: false,
					},
				},
				Usage: openai.EmbeddingUsage{
					PromptTokens: 3,
					TotalTokens:  3,
				},
			},
		},
		{
			name: "embedContent response with truncated flag",
			gcpResponse: `{
				"embedding": {"values": [0.4, 0.5, 0.6]},
				"usageMetadata": {"promptTokenCount": 7, "totalTokenCount": 7},
				"truncated": true
			}`,
			wantTokenUsage: tokenUsageFrom(7, -1, -1, -1, 7, -1),
			wantResponseBody: openai.EmbeddingResponse{
				Object: "list",
				Model:  "gemini-embedding-2-preview",
				Data: []openai.Embedding{
					{
						Object:    "embedding",
						Index:     0,
						Embedding: openai.EmbeddingUnion{Value: []float64{0.4, 0.5, 0.6}},
						Truncated: true,
					},
				},
				Usage: openai.EmbeddingUsage{
					PromptTokens: 7,
					TotalTokens:  7,
				},
			},
		},
		{
			name:           "embedContent response with no embedding",
			gcpResponse:    `{}`,
			wantTokenUsage: tokenUsageFrom(0, -1, -1, -1, 0, -1),
			wantResponseBody: openai.EmbeddingResponse{
				Object: "list",
				Model:  "gemini-embedding-2-preview",
				Data:   []openai.Embedding{},
				Usage: openai.EmbeddingUsage{
					PromptTokens: 0,
					TotalTokens:  0,
				},
			},
		},
		{
			name:        "embedContent response with invalid JSON",
			gcpResponse: `{"embedding": invalid json}`,
			wantError:   true,
		},
		{
			name: "embedContent response without usageMetadata",
			gcpResponse: `{
				"embedding": {"values": [0.1, 0.2, 0.3]}
			}`,
			wantTokenUsage: tokenUsageFrom(0, -1, -1, -1, 0, -1),
			wantResponseBody: openai.EmbeddingResponse{
				Object: "list",
				Model:  "gemini-embedding-2-preview",
				Data: []openai.Embedding{
					{
						Object:    "embedding",
						Index:     0,
						Embedding: openai.EmbeddingUnion{Value: []float64{0.1, 0.2, 0.3}},
						Truncated: false,
					},
				},
				Usage: openai.EmbeddingUsage{
					PromptTokens: 0,
					TotalTokens:  0,
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			translator := NewEmbeddingOpenAIToGCPVertexAITranslator("gemini-embedding-2-preview", "").(*openAIToGCPVertexAITranslatorV1Embedding)
			translator.requestModel = "gemini-embedding-2-preview"
			translator.useEmbedContent = true

			headerMut, bodyMut, tokenUsage, responseModel, err := translator.ResponseBody(
				map[string]string{"content-type": "application/json"},
				strings.NewReader(tc.gcpResponse),
				true,
				nil,
			)

			if tc.wantError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, headerMut)
			require.Len(t, headerMut, 1)
			require.Equal(t, contentLengthHeaderName, headerMut[0].Key())
			require.NotNil(t, bodyMut)
			require.Equal(t, "gemini-embedding-2-preview", responseModel)

			if diff := cmp.Diff(tc.wantTokenUsage, tokenUsage, cmp.AllowUnexported(metrics.TokenUsage{})); diff != "" {
				t.Errorf("TokenUsage mismatch (-want +got):\n%s", diff)
			}

			var actualResponse openai.EmbeddingResponse
			err = json.Unmarshal(bodyMut, &actualResponse)
			require.NoError(t, err)

			require.Equal(t, tc.wantResponseBody.Object, actualResponse.Object)
			require.Equal(t, tc.wantResponseBody.Model, actualResponse.Model)
			require.Equal(t, tc.wantResponseBody.Usage, actualResponse.Usage)
			require.Len(t, actualResponse.Data, len(tc.wantResponseBody.Data))

			for idx := range tc.wantResponseBody.Data {
				require.Equal(t, tc.wantResponseBody.Data[idx].Object, actualResponse.Data[idx].Object)
				require.Equal(t, tc.wantResponseBody.Data[idx].Index, actualResponse.Data[idx].Index)
				require.Equal(t, tc.wantResponseBody.Data[idx].Truncated, actualResponse.Data[idx].Truncated)

				wantEmbedding := tc.wantResponseBody.Data[idx].Embedding.Value.([]float64)
				actualEmbedding := actualResponse.Data[idx].Embedding.Value.([]float64)
				require.Len(t, actualEmbedding, len(wantEmbedding))

				for i, wantVal := range wantEmbedding {
					require.InDelta(t, wantVal, actualEmbedding[i], 1e-6, "Embedding %d value at index %d", idx, i)
				}
			}
		})
	}
}

func TestIsEmbedContentModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"gemini-embedding-2-preview", true},
		{"gemini-embedding-2-preview-0514", true},
		{"gemini-embedding-exp-03-07", true},
		{"gemini-embedding-001", false},
		{"text-embedding-004", false},
		{"text-embedding-005", false},
		{"some-maas-model", false},
		{"maas-embedding", false},
	}

	for _, tc := range tests {
		t.Run(tc.model, func(t *testing.T) {
			require.Equal(t, tc.want, isEmbedContentModel(tc.model))
		})
	}
}

// TestResponseModel_GCPVertexAIEmbeddings_EmbedContent tests the end-to-end flow for the embedContent path:
// RequestBody sets useEmbedContent, then ResponseBody uses it to parse the response correctly.
func TestResponseModel_GCPVertexAIEmbeddings_EmbedContent(t *testing.T) {
	modelName := "gemini-embedding-2-preview"
	translator := NewEmbeddingOpenAIToGCPVertexAITranslator(modelName, "")

	// Build request through RequestBody to set internal state.
	req := &openai.EmbeddingRequest{
		EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview"},
		OfCompletion: &openai.EmbeddingCompletionRequest{
			EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "gemini-embedding-2-preview"},
			Input:                openai.EmbeddingRequestInput{Value: "hello world"},
		},
	}
	reqBody, _ := json.Marshal(req)
	headers, _, err := translator.RequestBody(reqBody, req, false)
	require.NoError(t, err)

	// Verify embedContent path was selected.
	require.Equal(t, "publishers/google/models/gemini-embedding-2-preview:embedContent", headers[0].Value())

	// Simulate embedContent response (REST API returns singular "embedding", not plural "embeddings").
	embeddingResponse := `{
		"embedding": {"values": [0.1, 0.2, 0.3]},
		"usageMetadata": {"promptTokenCount": 5, "totalTokenCount": 5},
		"truncated": true
	}`

	_, respBody, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader([]byte(embeddingResponse)), true, nil)
	require.NoError(t, err)
	require.Equal(t, modelName, responseModel)

	// Verify token usage was extracted from usageMetadata.
	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(5), inputTokens)
	_, ok = tokenUsage.OutputTokens()
	require.False(t, ok)

	// Verify the response body contains correct data.
	var actualResponse openai.EmbeddingResponse
	err = json.Unmarshal(respBody, &actualResponse)
	require.NoError(t, err)
	require.Len(t, actualResponse.Data, 1)
	require.True(t, actualResponse.Data[0].Truncated)
	require.Equal(t, 5, actualResponse.Usage.PromptTokens)
	require.Equal(t, 5, actualResponse.Usage.TotalTokens)
}

// TestResponseModel_GCPVertexAIEmbeddings tests that GCP Vertex AI embeddings returns the request model
func TestResponseModel_GCPVertexAIEmbeddings(t *testing.T) {
	modelName := "text-embedding-004"
	translator := NewEmbeddingOpenAIToGCPVertexAITranslator(modelName, "")

	// Initialize translator with embedding request
	req := &openai.EmbeddingRequest{
		EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004"},
		OfCompletion: &openai.EmbeddingCompletionRequest{
			EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-004"},
			Input:                openai.EmbeddingRequestInput{Value: "test"},
		},
	}
	reqBody, _ := json.Marshal(req)
	_, _, err := translator.RequestBody(reqBody, req, false)
	require.NoError(t, err)

	// GCP VertexAI embedding response
	embeddingResponse := `{
		"predictions": [
			{
				"embeddings": {
					"values": [0.1, 0.2, 0.3],
					"statistics": {
						"token_count": 3,
						"truncated": false
					}
				}
			}
		]
	}`

	_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader([]byte(embeddingResponse)), true, nil)
	require.NoError(t, err)
	require.Equal(t, modelName, responseModel) // Returns the request model

	// Token usage should be provided from the statistics field
	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(3), inputTokens)
	_, ok = tokenUsage.OutputTokens()
	require.False(t, ok) // Output tokens not available for embeddings
}

func TestCollectPartsFromMessages(t *testing.T) {
	tests := []struct {
		name      string
		messages  []openai.ChatCompletionMessageParamUnion
		wantParts int
		wantError bool
	}{
		{
			name: "single user message with text",
			messages: []openai.ChatCompletionMessageParamUnion{
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    "user",
					Content: openai.StringOrUserRoleContentUnion{Value: "hello"},
				}},
			},
			wantParts: 1,
		},
		{
			name: "multiple user messages",
			messages: []openai.ChatCompletionMessageParamUnion{
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    "user",
					Content: openai.StringOrUserRoleContentUnion{Value: "first"},
				}},
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    "user",
					Content: openai.StringOrUserRoleContentUnion{Value: "second"},
				}},
			},
			wantParts: 2,
		},
		{
			name: "skip non-user roles",
			messages: []openai.ChatCompletionMessageParamUnion{
				{OfSystem: &openai.ChatCompletionSystemMessageParam{
					Role: "system",
				}},
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    "user",
					Content: openai.StringOrUserRoleContentUnion{Value: "user text"},
				}},
				{OfAssistant: &openai.ChatCompletionAssistantMessageParam{
					Role: "assistant",
				}},
			},
			wantParts: 1,
		},
		{
			name: "no user messages returns error",
			messages: []openai.ChatCompletionMessageParamUnion{
				{OfSystem: &openai.ChatCompletionSystemMessageParam{
					Role: "system",
				}},
			},
			wantError: true,
		},
		{
			name:      "empty messages returns error",
			messages:  []openai.ChatCompletionMessageParamUnion{},
			wantError: true,
		},
		{
			name: "user message with multimodal content",
			messages: []openai.ChatCompletionMessageParamUnion{
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role: "user",
					Content: openai.StringOrUserRoleContentUnion{Value: []openai.ChatCompletionContentPartUserUnionParam{
						{OfText: &openai.ChatCompletionContentPartTextParam{
							Type: "text",
							Text: "describe this",
						}},
						{OfImageURL: &openai.ChatCompletionContentPartImageParam{
							Type: "image_url",
							ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
								URL: "https://example.com/img.jpg",
							},
						}},
					}},
				}},
			},
			wantParts: 2,
		},
		{
			name: "user message with empty string content returns error",
			messages: []openai.ChatCompletionMessageParamUnion{
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    "user",
					Content: openai.StringOrUserRoleContentUnion{Value: ""},
				}},
			},
			wantError: true,
		},
		{
			name: "user message with invalid image URL returns error",
			messages: []openai.ChatCompletionMessageParamUnion{
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role: "user",
					Content: openai.StringOrUserRoleContentUnion{Value: []openai.ChatCompletionContentPartUserUnionParam{
						{OfImageURL: &openai.ChatCompletionContentPartImageParam{
							Type: "image_url",
							ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
								URL: "://invalid-url",
							},
						}},
					}},
				}},
			},
			wantError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parts, err := collectPartsFromMessages(tc.messages, "gemini-embedding-2-preview")
			if tc.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, parts, tc.wantParts)
		})
	}
}
