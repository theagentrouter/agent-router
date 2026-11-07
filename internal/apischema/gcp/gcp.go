// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package gcp

import (
	"time"

	"google.golang.org/genai"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
)

type GenerateContentRequest struct {
	// Contains the multipart content of a message.
	//
	// https://github.com/googleapis/go-genai/blob/6a8184fcaf8bf15f0c566616a7b356560309be9b/types.go#L858
	Contents []genai.Content `json:"contents"`
	// Tool details of a tool that the model may use to generate a response.
	//
	// https://github.com/googleapis/go-genai/blob/6a8184fcaf8bf15f0c566616a7b356560309be9b/types.go#L1406
	Tools []genai.Tool `json:"tools"`
	// Optional. Tool config.
	// This config is shared for all tools provided in the request.
	//
	// https://github.com/googleapis/go-genai/blob/6a8184fcaf8bf15f0c566616a7b356560309be9b/types.go#L1466
	ToolConfig *genai.ToolConfig `json:"toolConfig,omitempty"`
	// Optional. Generation config.
	// You can find API default values and more details at https://cloud.google.com/vertex-ai/generative-ai/docs/model-reference/inference#generationconfig
	// and https://cloud.google.com/vertex-ai/generative-ai/docs/multimodal/content-generation-parameters.
	GenerationConfig *genai.GenerationConfig `json:"generationConfig,omitempty"`
	// Optional. Instructions for the model to steer it toward better performance.
	// For example, "Answer as concisely as possible" or "Don't use technical
	// terms in your response".
	//
	// https://github.com/googleapis/go-genai/blob/6a8184fcaf8bf15f0c566616a7b356560309be9b/types.go#L858
	SystemInstruction *genai.Content `json:"systemInstruction,omitempty"`
	// Optional: Safety settings in the request to block unsafe content in the response.
	//
	// https://github.com/googleapis/go-genai/blob/6a8184fcaf8bf15f0c566616a7b356560309be9b/types.go#L1057
	SafetySettings []*genai.SafetySetting `json:"safetySettings,omitempty"`
	// Optional. The name of a pre-existing cached content resource to use as context for generation.
	// Format: "projects/{project}/locations/{location}/cachedContents/{cache_id}"
	//
	// https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/context-cache/context-cache-overview
	CachedContent string `json:"cachedContent,omitempty"`
}

// https://docs.cloud.google.com/vertex-ai/generative-ai/docs/model-reference/text-embeddings-api#syntax
type Instance struct {
	// The text that you want to generate embeddings for.
	Content string `json:"content"`

	// Used to convey intended downstream application to help the model produce better embeddings. If left blank, the default used is RETRIEVAL_QUERY.
	// For more information about task types, see https://docs.cloud.google.com/vertex-ai/generative-ai/docs/embeddings/task-types
	// https://docs.cloud.google.com/vertex-ai/generative-ai/docs/model-reference/text-embeddings-api#task_type
	TaskType openai.EmbeddingTaskType `json:"task_type,omitempty"`

	// Used to help the model produce better embeddings. Only valid with task_type=RETRIEVAL_DOCUMENT.
	Title string `json:"title,omitempty"`
}

// https://docs.cloud.google.com/vertex-ai/generative-ai/docs/model-reference/text-embeddings-api#parameter-list
type Parameters struct {
	// When set to true, input text will be truncated. When set to false, an error is returned if the input text is longer than the maximum length supported by the model. Defaults to true.
	AutoTruncate bool `json:"auto_truncate,omitempty"`

	// Used to specify output embedding size. If set, output embeddings will be truncated to the size specified.
	OutputDimensionality int `json:"outputDimensionality,omitempty"`
}

// https://github.com/googleapis/python-aiplatform/blob/30e41d01f3fd0ef08da6ad6eb7f83df34476105e/google/cloud/aiplatform_v1/types/prediction_service.py#L63
type PredictRequest struct {
	// A list of instances
	//
	Instances []*Instance `json:"instances"`

	// Optional configuration for the embedding request.
	// Uses the official genai library configuration structure.
	Parameters Parameters `json:"parameters,omitempty"`
}

// ContentEmbeddingStatistics contains statistics about the embedding.
// Note: We use custom struct instead of genai.ContentEmbeddingStatistics because
// the GCP API returns snake_case JSON fields (token_count), while the genai library
// uses camelCase (tokenCount).
// https://docs.cloud.google.com/vertex-ai/generative-ai/docs/model-reference/text-embeddings-api#response_body
type ContentEmbeddingStatistics struct {
	// The number of tokens in the input text.
	TokenCount int `json:"token_count,omitempty"`
	// Whether the input text was truncated.
	Truncated bool `json:"truncated,omitempty"`
}

// ContentEmbedding represents the embedding result from GCP Vertex AI.
// Note: We use custom struct instead of genai.ContentEmbedding to ensure
// correct JSON field names for statistics (snake_case vs camelCase).
type ContentEmbedding struct {
	// The embedding values.
	Values []float32 `json:"values,omitempty"`
	// Statistics about the embedding.
	Statistics *ContentEmbeddingStatistics `json:"statistics,omitempty"`
}

// https://docs.cloud.google.com/vertex-ai/generative-ai/docs/model-reference/text-embeddings-api#response_body
type Prediction struct {
	// The result generated from input text.
	Embeddings ContentEmbedding `json:"embeddings"`
}

// https://github.com/googleapis/python-aiplatform/blob/30e41d01f3fd0ef08da6ad6eb7f83df34476105e/google/cloud/aiplatform_v1/types/prediction_service.py#L117
type PredictResponse struct {
	Predictions []*Prediction `json:"predictions"`
}

// EmbedContentRequest is the request body for the embedContent endpoint used by newer embedding models
// (e.g. gemini-embedding-2-*).
// All input texts are packed as parts in a single Content object and we drop deprecated top-level fields
// (taskType, outputDimensionality, etc.)
//
// See https://docs.cloud.google.com/vertex-ai/docs/reference/rest/v1/projects.locations.publishers.models/embedContent
type EmbedContentRequest struct {
	Content genai.Content       `json:"content"`
	Config  *EmbedContentConfig `json:"embedContentConfig,omitempty"`
}

// EmbedContentConfig contains optional parameters for the embedContent method.
// https://github.com/googleapis/go-genai/blob/v1.54.0/models.go#L727
type EmbedContentConfig struct {
	TaskType             openai.EmbeddingTaskType `json:"taskType,omitempty"`
	Title                string                   `json:"title,omitempty"`
	OutputDimensionality int                      `json:"outputDimensionality,omitempty"`
	AutoTruncate         *bool                    `json:"autoTruncate,omitempty"`
}

// EmbedContentResponse is the response from the embedContent endpoint.
// The REST API returns a single embedding (not an array), plus usage metadata.
// https://docs.cloud.google.com/vertex-ai/docs/reference/rest/v1/projects.locations.publishers.models/embedContent
type EmbedContentResponse struct {
	// The embedding generated from the input content (singular object, not an array).
	Embedding *EmbedContentEmbedding `json:"embedding,omitempty"`
	// Usage metadata about the response.
	UsageMetadata *EmbedContentUsageMetadata `json:"usageMetadata,omitempty"`
	// Whether the input content was truncated before generating the embedding.
	Truncated bool `json:"truncated,omitempty"`
}

// EmbedContentEmbedding represents the embedding values from the embedContent response.
type EmbedContentEmbedding struct {
	// The embedding values.
	Values []float32 `json:"values,omitempty"`
}

// EmbedContentUsageMetadata contains token usage from the embedContent response.
type EmbedContentUsageMetadata struct {
	PromptTokenCount int `json:"promptTokenCount,omitempty"`
	TotalTokenCount  int `json:"totalTokenCount,omitempty"`
}

// CountTokenRequest represents the Vertex AI CountTokens API request.
// The Vertex AI API expects a flat structure (not nested config).
// The request-body fields (contents, tools, systemInstruction, generationConfig) are
// documented as optional in the REST API reference:
// https://cloud.google.com/vertex-ai/generative-ai/docs/reference/rest/v1/projects.locations.publishers.models/countTokens
type CountTokenRequest struct {
	// The content to count tokens for.
	// https://github.com/googleapis/go-genai/blob/6a8184fcaf8bf15f0c566616a7b356560309be9b/types.go#L858
	Contents []genai.Content `json:"contents"`

	// Optional. Instructions for the model to steer it toward better performance.
	SystemInstruction *genai.Content `json:"systemInstruction,omitempty"`

	// Optional. Code that enables the system to interact with external systems.
	Tools []genai.Tool `json:"tools,omitempty"`

	// Optional. Configuration that the model uses to generate the response.
	GenerationConfig *genai.GenerationConfig `json:"generationConfig,omitempty"`
}

// CreateCachedContent represents the request body for creating a cached content resource in GCP Vertex AI.
type CreateCachedContent struct {
	// Required. The model to use for generating the cached content.
	Model string `json:"model"`
	// Optional. The user-generated meaningful display name of the cached content.
	DisplayName string `json:"displayName,omitempty"`
	// Optional. The TTL for this resource. The expiration time is computed: now + TTL.
	// Value must be a string in GCP duration format, e.g. "300s".
	TTL string `json:"ttl,omitempty"`
	// Optional. Timestamp of when this resource is considered expired.
	// A pointer so that it is omitted when unset: omitempty has no effect on a
	// time.Time value, which would otherwise serialize as "0001-01-01T00:00:00Z".
	ExpireTime *time.Time `json:"expireTime,omitempty"`
	// The content to cache.
	Contents []genai.Content `json:"contents"`
	// Optional. Developer set system instruction.
	SystemInstruction *genai.Content `json:"systemInstruction,omitempty"`
	// Optional. A list of `Tools` the model may use to generate the next response.
	Tools []genai.Tool `json:"tools,omitempty"`
	// Optional. Configuration for the tools to use. This config is shared for all tools.
	ToolConfig *genai.ToolConfig `json:"toolConfig,omitempty"`
	// Optional. The Cloud KMS resource identifier of the customer managed
	// encryption key used to protect a resource.
	// The key needs to be in the same region as where the compute resource is
	// created. See
	// https://cloud.google.com/vertex-ai/docs/general/cmek for more
	// details. If this is set, then all created CachedContent objects
	// will be encrypted with the provided encryption key.
	// Allowed formats: projects/{project}/locations/{location}/keyRings/{key_ring}/cryptoKeys/{crypto_key}
	EncryptionSpec *EncryptionSpec `json:"encryption_spec,omitempty"`
}

// EncryptionSpec specifies the encryption key that will be used to protect the resource.
type EncryptionSpec struct {
	// Required. The Cloud KMS resource identifier of the customer managed
	// encryption key used to protect a resource.
	// The key needs to be in the same region as where the compute resource is
	// created. See
	// https://cloud.google.com/vertex-ai/docs/general/cmek for more
	// details. If this is set, then all created CachedContent objects
	// will be encrypted with the provided encryption key.
	// Allowed formats: projects/{project}/locations/{location}/keyRings/{key_ring}/cryptoKeys/{crypto_key}
	KmsKeyName string `json:"kmsKeyName"`
}

// A resource used in LLM queries for users to explicitly specify what to cache.
// https://github.com/googleapis/go-genai/blob/5fa73d012b899ad08135ce9439b88f592acdc5a8/types.go#L6476-L6492
type CachedContent struct {
	// Optional. The server-generated resource name of the cached content.
	Name string `json:"name,omitempty"`
	// Optional. The user-generated meaningful display name of the cached content.
	DisplayName string `json:"displayName,omitempty"`
	// Optional. The name of the publisher model to use for cached content.
	Model string `json:"model,omitempty"`
	// Optional. Creation time of the cache entry.
	CreateTime time.Time `json:"createTime,omitempty"`
	// Optional. When the cache entry was last updated in UTC time.
	UpdateTime time.Time `json:"updateTime,omitempty"`
	// Optional. Expiration time of the cached content.
	ExpireTime time.Time `json:"expireTime,omitempty"`
	// Optional. Metadata on the usage of the cached content.
	UsageMetadata *CachedContentUsageMetadata `json:"usageMetadata,omitempty"`
}

// Metadata on the usage of the cached content.
// https://github.com/googleapis/go-genai/blob/5fa73d012b899ad08135ce9439b88f592acdc5a8/types.go#L6462-L6474
type CachedContentUsageMetadata struct {
	// Duration of audio in seconds. This field is not supported in Gemini API.
	AudioDurationSeconds int32 `json:"audioDurationSeconds,omitempty"`
	// Number of images. This field is not supported in Gemini API.
	ImageCount int32 `json:"imageCount,omitempty"`
	// Number of text characters. This field is not supported in Gemini API.
	TextCount int32 `json:"textCount,omitempty"`
	// Total number of tokens that the cached content consumes.
	TotalTokenCount int32 `json:"totalTokenCount,omitempty"`
	// Duration of video in seconds. This field is not supported in Gemini API.
	VideoDurationSeconds int32 `json:"videoDurationSeconds,omitempty"`
}
