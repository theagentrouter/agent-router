// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package testopenai

import (
	"bytes"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
)

// EmbeddingsCassettes returns a slice of all cassettes for embeddings.
func EmbeddingsCassettes() []Cassette {
	return cassettes(embeddingsRequests)
}

var cassetteEmbeddingsBasic = &openai.EmbeddingRequest{
	Model: openai.ModelTextEmbedding3Small, EncodingFormat: new("float"),
	OfCompletion: &openai.EmbeddingCompletionRequest{
		Model: openai.ModelTextEmbedding3Small, EncodingFormat: new("float"),
		Input: openai.EmbeddingRequestInput{Value: "How do I reset my password?"},
	},
}

// embeddingsRequests contains the actual request body for each embeddings cassette.
var embeddingsRequests = map[Cassette]*openai.EmbeddingRequest{
	CassetteEmbeddingsBasic: cassetteEmbeddingsBasic,
	CassetteEmbeddingsBase64: {
		EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: openai.ModelTextEmbedding3Small, EncodingFormat: new("base64")},
		OfCompletion: &openai.EmbeddingCompletionRequest{
			Model: openai.ModelTextEmbedding3Small, EncodingFormat: new("base64"),
			Input: openai.EmbeddingRequestInput{Value: "How do I reset my password?"},
		},
	},
	CassetteEmbeddingsTokens: {
		EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: openai.ModelTextEmbedding3Small, EncodingFormat: new("base64")},
		OfCompletion: &openai.EmbeddingCompletionRequest{
			Model: openai.ModelTextEmbedding3Small, EncodingFormat: new("base64"),
			Input: openai.EmbeddingRequestInput{Value: []int64{4438, 656, 358, 7738, 856, 3636, 30}},
		},
	},
	CassetteEmbeddingsLargeText: {
		EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: openai.ModelTextEmbedding3Small, EncodingFormat: new("base64")},
		OfCompletion: &openai.EmbeddingCompletionRequest{
			Model: openai.ModelTextEmbedding3Small, EncodingFormat: new("base64"),
			Input: openai.EmbeddingRequestInput{
				Value: "The quick brown fox jumps over the lazy dog. This pangram sentence contains every letter of the English alphabet at least once. It has been used since at least the late 19th century to test typewriters and computer keyboards, display examples of fonts, and other applications involving text where the use of all letters in the alphabet is desired. The phrase is commonly used for touch-typing practice, testing typewriters and computer keyboards, and displaying examples of fonts. It is also used in other applications involving all the letters in the English alphabet.",
			},
		},
	},
	CassetteEmbeddingsUnknownModel: {
		EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: "text-embedding-4-ultra", EncodingFormat: new("base64")},
		OfCompletion: &openai.EmbeddingCompletionRequest{
			Model: "text-embedding-4-ultra", EncodingFormat: new("base64"), // Non-existent model.
			Input: openai.EmbeddingRequestInput{
				Value: "Test with unknown model",
			},
		},
	},
	CassetteEmbeddingsDimensions: {
		EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: openai.ModelTextEmbedding3Small, Dimensions: new(256), EncodingFormat: new("base64")},
		OfCompletion: &openai.EmbeddingCompletionRequest{
			Model: openai.ModelTextEmbedding3Small, Dimensions: new(256), EncodingFormat: new("base64"),
			Input: openai.EmbeddingRequestInput{
				Value: "Generate embeddings with specific dimensions",
			},
		},
	},
	CassetteEmbeddingsMaxTokens: {
		EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: openai.ModelTextEmbedding3Small, EncodingFormat: new("base64")},
		OfCompletion: &openai.EmbeddingCompletionRequest{
			Model: openai.ModelTextEmbedding3Small, EncodingFormat: new("base64"),
			Input: openai.EmbeddingRequestInput{
				// Near 8191 token limit for openai embeddings models.
				Value: generateLongText(7500),
			},
		},
	},
	CassetteEmbeddingsMixedBatch: {
		EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: openai.ModelTextEmbedding3Small, EncodingFormat: new("base64")},
		OfCompletion: &openai.EmbeddingCompletionRequest{
			Model: openai.ModelTextEmbedding3Small, EncodingFormat: new("base64"),
			Input: openai.EmbeddingRequestInput{
				Value: []string{
					"Hello 世界! 🌍",    // Mixed scripts and emoji.
					"Здравствуй мир", // Cyrillic.
					"مرحبا بالعالم",  // Arabic.
					"This is a much longer piece of text that contains multiple sentences. It tests how the embedding model handles varying input lengths within the same batch. The embeddings should capture the semantic meaning despite the length differences.",
					"🚀 Space emoji and symbols ✨ § ¶ †", // Special characters.
				},
			},
		},
	},
	CassetteEmbeddingsWhitespace: {
		EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: openai.ModelTextEmbedding3Small, EncodingFormat: new("base64")},
		OfCompletion: &openai.EmbeddingCompletionRequest{
			Model: openai.ModelTextEmbedding3Small, EncodingFormat: new("base64"),
			Input: openai.EmbeddingRequestInput{
				Value: []string{
					"   Leading spaces",
					"Trailing spaces   ",
					"Multiple   spaces   between   words",
					"\tTabs\tand\nnewlines\r\neverywhere",
					"  \n  \t  ", // Only whitespace.
				},
			},
		},
	},
	CassetteEmbeddingsBadRequest: {
		EmbeddingBaseRequest: openai.EmbeddingBaseRequest{Model: openai.ModelTextEmbedding3Small, EncodingFormat: new("invalid_format"), Dimensions: new(-1)},
		OfCompletion: &openai.EmbeddingCompletionRequest{
			Model: openai.ModelTextEmbedding3Small, EncodingFormat: new("invalid_format"), Dimensions: new(-1),
			Input: openai.EmbeddingRequestInput{
				// Above maximum value 100257 (inclusive).
				Value: []int64{102257},
			},
		},
	},
}

// generateLongText creates a long text string for testing token limits.
func generateLongText(approxChars int) string {
	// This simulates a realistic document that might be embedded.
	base := `In the field of natural language processing and machine learning, embeddings have become a fundamental representation technique. They transform discrete tokens, whether words, subwords, or characters, into dense vector representations in a continuous vector space. This transformation enables mathematical operations on text and captures semantic relationships between different pieces of text. `

	var result bytes.Buffer
	for result.Len() < approxChars {
		result.WriteString(base)
	}
	return result.String()[:approxChars]
}
