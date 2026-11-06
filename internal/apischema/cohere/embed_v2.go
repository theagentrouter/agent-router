// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package cohere contains Cohere API schema definitions.
package cohere

// EmbedV2Request represents the request body for Cohere Embed API v2.
// Docs: https://docs.cohere.com/reference/embed
type EmbedV2Request struct {
	// Model identifier to use, e.g. "embed-v4.0".
	Model string `json:"model"`
	// InputType specifies the type of input passed to the model. Required for embedding models v3 and higher.
	InputType EmbedV2InputType `json:"input_type"`
	// Texts is a list of strings to embed.
	Texts []string `json:"texts,omitempty"`
	// Images is a list of image data URIs to embed.
	Images []string `json:"images,omitempty"`
	// Inputs is a list of mixed text/image content entries to embed.
	Inputs []EmbedV2Inputs `json:"inputs,omitempty"`
	// EmbeddingTypes specifies the desired output format(s).
	EmbeddingTypes []EmbedV2EmbeddingType `json:"embedding_types,omitempty"`
	// MaxTokens is the maximum number of tokens to embed per input.
	MaxTokens *int `json:"max_tokens,omitempty"`
	// OutputDimension is the number of dimensions of the output embedding.
	OutputDimension *int `json:"output_dimension,omitempty"`
	// Truncate specifies how the API will handle inputs longer than the maximum token length.
	Truncate *EmbedV2Truncate `json:"truncate,omitempty"`
	// Priority controls request processing order.
	Priority *int `json:"priority,omitempty"`
}

// EmbedV2InputType specifies the type of input passed to the embed endpoint.
type EmbedV2InputType string

const (
	// EmbedV2InputTypeSearchDocument is used for embeddings stored in a vector database for search use-cases.
	EmbedV2InputTypeSearchDocument EmbedV2InputType = "search_document"
	// EmbedV2InputTypeSearchQuery is used for embeddings of search queries run against a vector database.
	EmbedV2InputTypeSearchQuery EmbedV2InputType = "search_query"
	// EmbedV2InputTypeClassification is used for embeddings passed through a text classifier.
	EmbedV2InputTypeClassification EmbedV2InputType = "classification"
	// EmbedV2InputTypeClustering is used for embeddings run through a clustering algorithm.
	EmbedV2InputTypeClustering EmbedV2InputType = "clustering"
	// EmbedV2InputTypeImage is used for embeddings of images.
	EmbedV2InputTypeImage EmbedV2InputType = "image"
)

// EmbedV2EmbeddingType specifies the desired output format(s) of the embed endpoint.
type EmbedV2EmbeddingType string

const (
	EmbedV2EmbeddingTypeFloat   EmbedV2EmbeddingType = "float"
	EmbedV2EmbeddingTypeInt8    EmbedV2EmbeddingType = "int8"
	EmbedV2EmbeddingTypeUint8   EmbedV2EmbeddingType = "uint8"
	EmbedV2EmbeddingTypeBinary  EmbedV2EmbeddingType = "binary"
	EmbedV2EmbeddingTypeUbinary EmbedV2EmbeddingType = "ubinary"
	EmbedV2EmbeddingTypeBase64  EmbedV2EmbeddingType = "base64"
)

// EmbedV2Truncate specifies how the API will handle inputs longer than the maximum token length.
type EmbedV2Truncate string

const (
	EmbedV2TruncateNone  EmbedV2Truncate = "NONE"
	EmbedV2TruncateStart EmbedV2Truncate = "START"
	EmbedV2TruncateEnd   EmbedV2Truncate = "END"
)

// EmbedV2Inputs represents an array of mixed text/image input entries.
type EmbedV2Inputs struct {
	Content []EmbedV2InputContent `json:"content"`
}

// EmbedV2InputContent represents a single content item in a mixed input entry.
type EmbedV2InputContent struct {
	Type     string           `json:"type"`
	Text     *string          `json:"text,omitempty"`
	ImageURL *EmbedV2ImageURL `json:"image_url,omitempty"`
}

// EmbedV2ImageURL represents a base64-encoded image data.
type EmbedV2ImageURL struct {
	URL string `json:"url"`
}

// EmbedV2Response represents the response from Cohere Embed API v2.
// Docs: https://docs.cohere.com/reference/embed
type EmbedV2Response struct {
	// Unique request ID.
	ID *string `json:"id,omitempty"`
	// Embeddings holds the resulting embedding vectors, keyed by the embedding type.
	Embeddings *EmbedV2Embeddings `json:"embeddings,omitempty"`
	// Texts is the list of input texts that were embedded.
	Texts []string `json:"texts,omitempty"`
	// Images is the list of input images that were embedded.
	Images []EmbedV2ImageMeta `json:"images,omitempty"`
	// Additional metadata including API version and billing.
	Meta *EmbedV2Meta `json:"meta,omitempty"`
}

// EmbedV2Embeddings holds the resulting embedding vectors, keyed by the embedding type.
type EmbedV2Embeddings struct {
	Float   [][]float64 `json:"float,omitempty"`
	Int8    [][]int8    `json:"int8,omitempty"`
	Uint8   [][]uint8   `json:"uint8,omitempty"`
	Binary  [][]byte    `json:"binary,omitempty"`
	Ubinary [][]byte    `json:"ubinary,omitempty"`
	Base64  []string    `json:"base64,omitempty"`
}

// EmbedV2ImageMeta contains metadata about an input image.
type EmbedV2ImageMeta struct {
	Width    *int    `json:"width,omitempty"`
	Height   *int    `json:"height,omitempty"`
	Format   *string `json:"format,omitempty"`
	BitDepth *int    `json:"bit_depth,omitempty"`
}

// EmbedV2Meta contains metadata returned by the API.
type EmbedV2Meta struct {
	// APIVersion contains the version information for the API that processed the request.
	APIVersion *EmbedV2APIVersion `json:"api_version,omitempty"`
	// BilledUnits reports the billed resource usage for this request.
	BilledUnits *EmbedV2BilledUnits `json:"billed_units,omitempty"`
	// Tokens provides the token usage breakdown for the request/response.
	Tokens *EmbedV2Tokens `json:"tokens,omitempty"`
	// CachedTokens is the number of prompt tokens that hit the inference cache.
	CachedTokens *float64 `json:"cached_tokens,omitempty"`
	// Warnings contains any non-fatal warnings generated while processing the request.
	Warnings []string `json:"warnings,omitempty"`
}

// EmbedV2APIVersion describes the API version details in the response meta.
type EmbedV2APIVersion struct {
	// Version is the API version string (e.g., "2").
	Version string `json:"version"`
	// IsDeprecated indicates whether this API version is deprecated (nullable).
	IsDeprecated *bool `json:"is_deprecated,omitempty"`
	// IsExperimental indicates whether this API version is experimental (nullable).
	IsExperimental *bool `json:"is_experimental,omitempty"`
}

// EmbedV2BilledUnits contains usage metrics related to the request.
type EmbedV2BilledUnits struct {
	// Images is the number of billed images (nullable).
	Images *float64 `json:"images,omitempty"`
	// InputTokens is the number of billed input tokens (nullable).
	InputTokens *float64 `json:"input_tokens,omitempty"`
	// ImageTokens is the number of billed image tokens (nullable).
	ImageTokens *float64 `json:"image_tokens,omitempty"`
	// OutputTokens is the number of billed output tokens (nullable).
	OutputTokens *float64 `json:"output_tokens,omitempty"`
	// SearchUnits is the number of billed search units (nullable).
	SearchUnits *float64 `json:"search_units,omitempty"`
	// Classifications is the number of billed classification units (nullable).
	Classifications *float64 `json:"classifications,omitempty"`
}

// EmbedV2Tokens captures token accounting for the request.
// Docs: https://docs.cohere.com/reference/embed#response.body.meta.tokens
type EmbedV2Tokens struct {
	// InputTokens is the number of tokens used as input to the model (nullable).
	InputTokens *float64 `json:"input_tokens,omitempty"`
	// OutputTokens is the number of tokens produced by the model (nullable).
	OutputTokens *float64 `json:"output_tokens,omitempty"`
}

// EmbedV2Error describes a Cohere v2 error.
type EmbedV2Error struct {
	// ID is a unique identifier for the error (nullable).
	ID *string `json:"id,omitempty"`
	// Message is a human-readable description of the error (nullable).
	Message *string `json:"message,omitempty"`
}
