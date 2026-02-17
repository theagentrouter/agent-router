// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package gcp

import (
	"fmt"
	"net/http"
	"reflect"
	"time"

	"cloud.google.com/go/civil"
	"google.golang.org/genai"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
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

// Job state.
type JobState string

// Job error.
type JobError struct {
	// A list of messages that carry the error details. There is a common set of message
	// types for APIs to use.
	Details []string `json:"details,omitempty"`
	// The status code.
	Code *int32 `json:"code,omitempty"`
	// A developer-facing error message, which should be in English. Any user-facing error
	// message should be localized and sent in the `details` field.
	Message string `json:"message,omitempty"`
}

// The tokenization quality used for given media.
type PartMediaResolutionLevel string

// Media resolution for the input media.
type PartMediaResolution struct {
	// Optional. The tokenization quality used for given media.
	Level PartMediaResolutionLevel `json:"level,omitempty"`
	// Optional. Specifies the required sequence length for media tokenization.
	NumTokens *int32 `json:"numTokens,omitempty"`
}

// Outcome of the code execution.
type Outcome string

// Result of executing the [ExecutableCode]. Only generated when using the [CodeExecution]
// tool, and always follows a `part` containing the [ExecutableCode].
type CodeExecutionResult struct {
	// Required. Outcome of the code execution.
	Outcome Outcome `json:"outcome,omitempty"`
	// Optional. Contains stdout when code execution is successful, stderr or other description
	// otherwise.
	Output string `json:"output,omitempty"`
}

// Programming language of the `code`.
type Language string

// Code generated by the model that is meant to be executed, and the result returned
// to the model. Generated when using the [CodeExecution] tool, in which the code will
// be automatically executed, and a corresponding [CodeExecutionResult] will also be
// generated.
type ExecutableCode struct {
	// Required. The code to be executed.
	Code string `json:"code,omitempty"`
	// Required. Programming language of the `code`.
	Language Language `json:"language,omitempty"`
}

// URI based data.
type FileData struct {
	// Optional. Display name of the file data. Used to provide a label or filename to distinguish
	// file datas. This field is only returned in PromptMessage for prompt management. It
	// is currently used in the Gemini GenerateContent calls only when server side tools
	// (code_execution, google_search, and url_context) are enabled. This field is not supported
	// in Gemini API.
	DisplayName string `json:"displayName,omitempty"`
	// Required. URI.
	FileURI string `json:"fileUri,omitempty"`
	// Required. The IANA standard MIME type of the source data.
	MIMEType string `json:"mimeType,omitempty"`
}

// Partial argument value of the function call. This data type is not supported in Gemini
// API.
type PartialArg struct {
	// Optional. Represents a NULL value.
	NULLValue string `json:"nullValue,omitempty"`
	// Optional. Represents a double value.
	NumberValue *float64 `json:"numberValue,omitempty"`
	// Optional. Represents a string value.
	StringValue string `json:"stringValue,omitempty"`
	// Optional. Represents a boolean value.
	BoolValue *bool `json:"boolValue,omitempty"`
	// Required. A JSON Path (RFC 9535) to the argument being streamed. https://datatracker.ietf.org/doc/html/rfc9535.
	// e.g. "$.foo.bar[0].data".
	JsonPath string `json:"jsonPath,omitempty"`
	// Optional. Whether this is not the last part of the same json_path. If true, another
	// PartialArg message for the current json_path is expected to follow.
	WillContinue *bool `json:"willContinue,omitempty"`
}

// A function call.
type FunctionCall struct {
	// Optional. The unique ID of the function call. If populated, the client to execute
	// the
	// `function_call` and return the response with the matching `id`.
	ID string `json:"id,omitempty"`
	// Optional. The function parameters and values in JSON object format. See [FunctionDeclaration.parameters]
	// for parameter details.
	Args map[string]any `json:"args,omitempty"`
	// Optional. Required. The name of the function to call. Matches [FunctionDeclaration.Name].
	Name string `json:"name,omitempty"`
	// Optional. The partial argument value of the function call. If provided, represents
	// the arguments/fields that are streamed incrementally. This field is not supported
	// in Gemini API.
	PartialArgs []*PartialArg `json:"partialArgs,omitempty"`
	// Optional. Whether this is the last part of the FunctionCall. If true, another partial
	// message for the current FunctionCall is expected to follow. This field is not supported
	// in Gemini API.
	WillContinue *bool `json:"willContinue,omitempty"`
}

// Specifies how the response should be scheduled in the conversation.
type FunctionResponseScheduling string

// Raw media bytes for function response.
// Text should not be sent as raw bytes, use the FunctionResponse.response
// field.
type FunctionResponseBlob struct {
	// Required. The IANA standard MIME type of the source data.
	MIMEType string `json:"mimeType,omitempty"`
	// Required. Inline media bytes.
	Data []byte `json:"data,omitempty"`
	// Optional. Display name of the blob.
	// Used to provide a label or filename to distinguish blobs.
	DisplayName string `json:"displayName,omitempty"`
}

// URI based data for function response.
type FunctionResponseFileData struct {
	// Required. URI.
	FileURI string `json:"fileUri,omitempty"`
	// Required. The IANA standard MIME type of the source data.
	MIMEType string `json:"mimeType,omitempty"`
	// Optional. Display name of the file.
	// Used to provide a label or filename to distinguish files.
	DisplayName string `json:"displayName,omitempty"`
}

// A datatype containing media that is part of a `FunctionResponse` message.
// A `FunctionResponsePart` consists of data which has an associated datatype. A
// `FunctionResponsePart` can only contain one of the accepted types in
// `FunctionResponsePart.data`.
// A `FunctionResponsePart` must have a fixed IANA MIME type identifying the
// type and subtype of the media if the `inline_data` field is filled with raw
// bytes.
type FunctionResponsePart struct {
	// Optional. Inline media bytes.
	InlineData *FunctionResponseBlob `json:"inlineData,omitempty"`
	// Optional. URI based data.
	FileData *FunctionResponseFileData `json:"fileData,omitempty"`
}

// A function response.
type FunctionResponse struct {
	// Optional. Signals that function call continues, and more responses will be returned,
	// turning the function call into a generator. Is only applicable to NON_BLOCKING function
	// calls (see FunctionDeclaration.behavior for details), ignored otherwise. If false,
	// the default, future responses will not be considered. Is only applicable to NON_BLOCKING
	// function calls, is ignored otherwise. If set to false, future responses will not
	// be considered. It is allowed to return empty `response` with `will_continue=False`
	// to signal that the function call is finished.
	WillContinue *bool `json:"willContinue,omitempty"`
	// Optional. Specifies how the response should be scheduled in the conversation. Only
	// applicable to NON_BLOCKING function calls, is ignored otherwise. Defaults to WHEN_IDLE.
	Scheduling FunctionResponseScheduling `json:"scheduling,omitempty"`
	// Optional. List of parts that constitute a function response. Each part may
	// have a different IANA MIME type.
	Parts []*FunctionResponsePart `json:"parts,omitempty"`
	// Optional. The ID of the function call this response is for. Populated by the client
	// to match the corresponding function call `id`.
	ID string `json:"id,omitempty"`
	// Required. The name of the function to call. Matches [FunctionDeclaration.name] and
	// [FunctionCall.name].
	Name string `json:"name,omitempty"`
	// Required. The function response in JSON object format. Use "output" key to specify
	// function output and "error" key to specify error details (if any). If "output" and
	// "error" keys are not specified, then whole "response" is treated as function output.
	Response map[string]any `json:"response,omitempty"`
}

// Content blob.
type Blob struct {
	// Required. Raw bytes.
	Data []byte `json:"data,omitempty"`
	// Optional. Display name of the blob. Used to provide a label or filename to distinguish
	// blobs. This field is only returned in PromptMessage for prompt management. It is
	// currently used in the Gemini GenerateContent calls only when server side tools (code_execution,
	// google_search, and url_context) are enabled. This field is not supported in Gemini
	// API.
	DisplayName string `json:"displayName,omitempty"`
	// Required. The IANA standard MIME type of the source data.
	MIMEType string `json:"mimeType,omitempty"`
}

// Metadata describes the input video content.
type VideoMetadata struct {
	// Optional. The end offset of the video.
	EndOffset time.Duration `json:"endOffset,omitempty"`
	// Optional. The frame rate of the video sent to the model. If not specified, the default
	// value will be 1.0. The FPS range is (0.0, 24.0].
	FPS *float64 `json:"fps,omitempty"`
	// Optional. The start offset of the video.
	StartOffset time.Duration `json:"startOffset,omitempty"`
}

func (c *VideoMetadata) UnmarshalJSON(data []byte) error {
	type Alias VideoMetadata
	aux := &struct {
		EndOffset   string `json:"endOffset,omitempty"`
		StartOffset string `json:"startOffset,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(c),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	if aux.EndOffset != "" {
		d, err := time.ParseDuration(aux.EndOffset)
		if err != nil {
			return err
		}
		c.EndOffset = d
	}

	if aux.StartOffset != "" {
		d, err := time.ParseDuration(aux.StartOffset)
		if err != nil {
			return err
		}
		c.StartOffset = d
	}

	return nil
}

func (c *VideoMetadata) MarshalJSON() ([]byte, error) {
	type Alias VideoMetadata
	aux := &struct {
		EndOffset   string `json:"endOffset,omitempty"`
		StartOffset string `json:"startOffset,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(c),
	}

	if c.StartOffset != 0 {
		aux.StartOffset = fmt.Sprintf("%.0fs", c.StartOffset.Seconds())
	}
	if c.EndOffset != 0 {
		aux.EndOffset = fmt.Sprintf("%.0fs", c.EndOffset.Seconds())
		if aux.StartOffset == "" {
			aux.StartOffset = "0s"
		}
	}

	return json.Marshal(aux)
}

// A datatype containing media content.
// Exactly one field within a Part should be set, representing the specific type
// of content being conveyed. Using multiple fields within the same `Part`
// instance is considered invalid.
type Part struct {
	// Optional. Media resolution for the input media.
	MediaResolution *PartMediaResolution `json:"mediaResolution,omitempty"`
	// Optional. Result of executing the [ExecutableCode].
	CodeExecutionResult *CodeExecutionResult `json:"codeExecutionResult,omitempty"`
	// Optional. Code generated by the model that is meant to be executed.
	ExecutableCode *ExecutableCode `json:"executableCode,omitempty"`
	// Optional. URI based data.
	FileData *FileData `json:"fileData,omitempty"`
	// Optional. A predicted [FunctionCall] returned from the model that contains a string
	// representing the [FunctionDeclaration.Name] with the parameters and their values.
	FunctionCall *FunctionCall `json:"functionCall,omitempty"`
	// Optional. The result output of a [FunctionCall] that contains a string representing
	// the [FunctionDeclaration.Name] and a structured JSON object containing any output
	// from the function call. It is used as context to the model.
	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`
	// Optional. Inlined bytes data.
	InlineData *Blob `json:"inlineData,omitempty"`
	// Optional. Text part (can be code).
	Text string `json:"text,omitempty"`
	// Optional. Indicates if the part is thought from the model.
	Thought bool `json:"thought,omitempty"`
	// Optional. An opaque signature for the thought so it can be reused in subsequent requests.
	ThoughtSignature []byte `json:"thoughtSignature,omitempty"`
	// Optional. Video metadata. The metadata should only be specified while the video data
	// is presented in inline_data or file_data.
	VideoMetadata *VideoMetadata `json:"videoMetadata,omitempty"`
}

// Contains the multi-part content of a message.
type Content struct {
	// Optional. List of parts that constitute a single message. Each part may have
	// a different IANA MIME type.
	Parts []*Part `json:"parts,omitempty"`
	// Optional. The producer of the content. Must be either 'user' or 'model'. Useful to
	// set for multi-turn conversations, otherwise can be left blank or unset.
	Role string `json:"role,omitempty"`
}

// ExtrasRequestProvider provides a way to dynamically modify the request body
// before it is sent. It is a function that takes the request body and returns
// the modified body. This is useful for advanced scenarios where request
// parameters need to be added based on logic that cannot
// be handled by a static map.
type ExtrasRequestProvider = func(body map[string]any) map[string]any

// HTTP options to be used in each of the requests.
type HTTPOptions struct {
	// Optional. BaseURL specifies the base URL for the API endpoint. If empty, defaults
	// to "https://generativelanguage.googleapis.com/" for the Gemini API backend, and location-specific
	// Vertex AI endpoint (e.g., "https://us-central1-aiplatform.googleapis.com/
	BaseURL string `json:"baseUrl,omitempty"`
	// Optional. APIVersion specifies the version of the API to use. If empty, defaults
	// to "v1beta" for Gemini API and "v1beta1" for Vertex AI.
	APIVersion string `json:"apiVersion,omitempty"`
	// Optional. Additional HTTP headers to be sent with the request.
	Headers http.Header `json:"headers,omitempty"`
	// Optional. Timeout for the request in milliseconds.
	Timeout *time.Duration `json:"timeout,omitempty"`
	// Optional. Extra parameters to add to the request body.
	// The structure must match the backend API's request structure.
	//   - VertexAI backend API docs: https://cloud.google.com/vertex-ai/docs/reference/rest
	//   - GeminiAPI backend API docs: https://ai.google.dev/api/rest
	ExtraBody map[string]any `json:"extraBody,omitempty"`
	// Optional. A function that allows for request body customization.
	// It is executed after ExtraBody has been merged, offering more advanced
	// control over the request body than the static ExtraBody.
	ExtrasRequestProvider ExtrasRequestProvider `json:"-"`
}

// The type of the data.
type Type string

// Schema is used to define the format of input/output data.
// Represents a select subset of an [OpenAPI 3.0 schema
// object](https://spec.openapis.org/oas/v3.0.3#schema-object). More fields may
// be added in the future as needed.
// You can find more details and examples at https://spec.openapis.org/oas/v3.0.3.html#schema-object
type Schema struct {
	// Optional. The value should be validated against any (one or more) of the subschemas
	// in the list.
	AnyOf []*Schema `json:"anyOf,omitempty"`
	// Optional. Default value of the data.
	Default any `json:"default,omitempty"`
	// Optional. The description of the data.
	Description string `json:"description,omitempty"`
	// Optional. Possible values of the element of primitive type with enum format. Examples:
	// 1. We can define direction as : {type:STRING, format:enum, enum:["EAST", NORTH",
	// "SOUTH", "WEST"]} 2. We can define apartment number as : {type:INTEGER, format:enum,
	// enum:["101", "201", "301"]}
	Enum []string `json:"enum,omitempty"`
	// Optional. Example of the object. Will only populated when the object is the root.
	Example any `json:"example,omitempty"`
	// Optional. The format of the data. Supported formats: for NUMBER type: "float", "double"
	// for INTEGER type: "int32", "int64" for STRING type: "email", "byte", etc
	Format string `json:"format,omitempty"`
	// Optional. SCHEMA FIELDS FOR TYPE ARRAY Schema of the elements of Type.ARRAY.
	Items *Schema `json:"items,omitempty"`
	// Optional. Maximum number of the elements for Type.ARRAY.
	MaxItems *int64 `json:"maxItems,omitempty"`
	// Optional. Maximum length of the Type.STRING
	MaxLength *int64 `json:"maxLength,omitempty"`
	// Optional. Maximum number of the properties for Type.OBJECT.
	MaxProperties *int64 `json:"maxProperties,omitempty"`
	// Optional. Maximum value of the Type.INTEGER and Type.NUMBER
	Maximum *float64 `json:"maximum,omitempty"`
	// Optional. Minimum number of the elements for Type.ARRAY.
	MinItems *int64 `json:"minItems,omitempty"`
	// Optional. SCHEMA FIELDS FOR TYPE STRING Minimum length of the Type.STRING
	MinLength *int64 `json:"minLength,omitempty"`
	// Optional. Minimum number of the properties for Type.OBJECT.
	MinProperties *int64 `json:"minProperties,omitempty"`
	// Optional. Minimum value of the Type.INTEGER and Type.NUMBER.
	Minimum *float64 `json:"minimum,omitempty"`
	// Optional. Indicates if the value may be null.
	Nullable *bool `json:"nullable,omitempty"`
	// Optional. Pattern of the Type.STRING to restrict a string to a regular expression.
	Pattern string `json:"pattern,omitempty"`
	// Optional. SCHEMA FIELDS FOR TYPE OBJECT Properties of Type.OBJECT.
	Properties map[string]*Schema `json:"properties,omitempty"`
	// Optional. The order of the properties. Not a standard field in open API spec. Only
	// used to support the order of the properties.
	PropertyOrdering []string `json:"propertyOrdering,omitempty"`
	// Optional. Required properties of Type.OBJECT.
	Required []string `json:"required,omitempty"`
	// Optional. The title of the Schema.
	Title string `json:"title,omitempty"`
	// Optional. The type of the data.
	Type Type `json:"type,omitempty"`
}

// When automated routing is specified, the routing will be determined by the pretrained
// routing model and customer provided model routing preference. This data type is not
// supported in Gemini API.
type GenerationConfigRoutingConfigAutoRoutingMode struct {
	// The model routing preference.
	ModelRoutingPreference string `json:"modelRoutingPreference,omitempty"`
}

// When manual routing is set, the specified model will be used directly. This data
// type is not supported in Gemini API.
type GenerationConfigRoutingConfigManualRoutingMode struct {
	// The model name to use. Only the public LLM models are accepted. See [Supported models](https://cloud.google.com/vertex-ai/generative-ai/docs/model-reference/inference#supported-models).
	ModelName string `json:"modelName,omitempty"`
}

// The configuration for routing the request to a specific model. This data type is
// not supported in Gemini API.
type GenerationConfigRoutingConfig struct {
	// Automated routing.
	AutoMode *GenerationConfigRoutingConfigAutoRoutingMode `json:"autoMode,omitempty"`
	// Manual routing.
	ManualMode *GenerationConfigRoutingConfigManualRoutingMode `json:"manualMode,omitempty"`
}

// Options for feature selection preference.
type FeatureSelectionPreference string

// Config for model selection.
type ModelSelectionConfig struct {
	// Optional. Options for feature selection preference.
	FeatureSelectionPreference FeatureSelectionPreference `json:"featureSelectionPreference,omitempty"`
}

// Harm category.
type HarmCategory string

// Specify if the threshold is used for probability or severity score. If not specified,
// the threshold is used for probability score. This enum is not supported in Gemini
// API.
type HarmBlockMethod string

// The harm block threshold.
type HarmBlockThreshold string

// Safety settings.
type SafetySetting struct {
	// Required. Harm category.
	Category HarmCategory `json:"category,omitempty"`
	// Optional. Specify if the threshold is used for probability or severity score. If
	// not specified, the threshold is used for probability score. This field is not supported
	// in Gemini API.
	Method HarmBlockMethod `json:"method,omitempty"`
	// Required. The harm block threshold.
	Threshold HarmBlockThreshold `json:"threshold,omitempty"`
}

// The API secret. This data type is not supported in Gemini API.
type APIAuthAPIKeyConfig struct {
	// Required. The SecretManager secret version resource name storing API key. e.g. projects/{project}/secrets/{secret}/versions/{version}
	APIKeySecretVersion string `json:"apiKeySecretVersion,omitempty"`
	// The API key string. Either this or `api_key_secret_version` must be set.
	APIKeyString string `json:"apiKeyString,omitempty"`
}

// The generic reusable API auth config. Deprecated. Please use AuthConfig (google/cloud/aiplatform/master/auth.proto)
// instead. This data type is not supported in Gemini API.
type APIAuth struct {
	// The API secret.
	APIKeyConfig *APIAuthAPIKeyConfig `json:"apiKeyConfig,omitempty"`
}

// The location of the API key. This enum is not supported in Gemini API.
type HTTPElementLocation string

// Config for authentication with API key. This data type is not supported in Gemini
// API.
type APIKeyConfig struct {
	// Optional. The name of the SecretManager secret version resource storing the API key.
	// Format: `projects/{project}/secrets/{secrete}/versions/{version}` - If both `api_key_secret`
	// and `api_key_string` are specified, this field takes precedence over `api_key_string`.
	// - If specified, the `secretmanager.versions.access` permission should be granted
	// to Vertex AI Extension Service Agent (https://cloud.google.com/vertex-ai/docs/general/access-control#service-agents)
	// on the specified resource.
	APIKeySecret string `json:"apiKeySecret,omitempty"`
	// Optional. The API key to be used in the request directly.
	APIKeyString string `json:"apiKeyString,omitempty"`
	// Optional. The location of the API key.
	HTTPElementLocation HTTPElementLocation `json:"httpElementLocation,omitempty"`
	// Optional. The parameter name of the API key. E.g. If the API request is "https://example.com/act?api_key=",
	// "api_key" would be the parameter name.
	Name string `json:"name,omitempty"`
}

// Type of auth scheme. This enum is not supported in Gemini API.
type AuthType string

// Config for Google Service Account Authentication. This data type is not supported
// in Gemini API.
type AuthConfigGoogleServiceAccountConfig struct {
	// Optional. The service account that the extension execution service runs as. - If
	// the service account is specified, the `iam.serviceAccounts.getAccessToken` permission
	// should be granted to Vertex AI Extension Service Agent (https://cloud.google.com/vertex-ai/docs/general/access-control#service-agents)
	// on the specified service account. - If not specified, the Vertex AI Extension Service
	// Agent will be used to execute the Extension.
	ServiceAccount string `json:"serviceAccount,omitempty"`
}

// Config for HTTP Basic Authentication. This data type is not supported in Gemini API.
type AuthConfigHTTPBasicAuthConfig struct {
	// Required. The name of the SecretManager secret version resource storing the base64
	// encoded credentials. Format: `projects/{project}/secrets/{secrete}/versions/{version}`
	// - If specified, the `secretmanager.versions.access` permission should be granted
	// to Vertex AI Extension Service Agent (https://cloud.google.com/vertex-ai/docs/general/access-control#service-agents)
	// on the specified resource.
	CredentialSecret string `json:"credentialSecret,omitempty"`
}

// Config for user oauth. This data type is not supported in Gemini API.
type AuthConfigOauthConfig struct {
	// Access token for extension endpoint. Only used to propagate token from [[ExecuteExtensionRequest.runtime_auth_config]]
	// at request time.
	AccessToken string `json:"accessToken,omitempty"`
	// The service account used to generate access tokens for executing the Extension. -
	// If the service account is specified, the `iam.serviceAccounts.getAccessToken` permission
	// should be granted to Vertex AI Extension Service Agent (https://cloud.google.com/vertex-ai/docs/general/access-control#service-agents)
	// on the provided service account.
	ServiceAccount string `json:"serviceAccount,omitempty"`
}

// Config for user OIDC auth. This data type is not supported in Gemini API.
type AuthConfigOidcConfig struct {
	// OpenID Connect formatted ID token for extension endpoint. Only used to propagate
	// token from [[ExecuteExtensionRequest.runtime_auth_config]] at request time.
	IDToken string `json:"idToken,omitempty"`
	// The service account used to generate an OpenID Connect (OIDC)-compatible JWT token
	// signed by the Google OIDC Provider (accounts.google.com) for extension endpoint (https://cloud.google.com/iam/docs/create-short-lived-credentials-direct#sa-credentials-oidc).
	// - The audience for the token will be set to the URL in the server URL defined in
	// the OpenAPI spec. - If the service account is provided, the service account should
	// grant `iam.serviceAccounts.getOpenIDToken` permission to Vertex AI Extension Service
	// Agent (https://cloud.google.com/vertex-ai/docs/general/access-control#service-agents).
	ServiceAccount string `json:"serviceAccount,omitempty"`
}

// Auth configuration to run the extension. This data type is not supported in Gemini
// API.
type AuthConfig struct {
	// Config for API key auth.
	APIKeyConfig *APIKeyConfig `json:"apiKeyConfig,omitempty"`
	// Type of auth scheme.
	AuthType AuthType `json:"authType,omitempty"`
	// Config for Google Service Account auth.
	GoogleServiceAccountConfig *AuthConfigGoogleServiceAccountConfig `json:"googleServiceAccountConfig,omitempty"`
	// Config for HTTP Basic auth.
	HTTPBasicAuthConfig *AuthConfigHTTPBasicAuthConfig `json:"httpBasicAuthConfig,omitempty"`
	// Config for user oauth.
	OauthConfig *AuthConfigOauthConfig `json:"oauthConfig,omitempty"`
	// Config for user OIDC auth.
	OidcConfig *AuthConfigOidcConfig `json:"oidcConfig,omitempty"`
}

// The API spec that the external API implements. This enum is not supported in Gemini
// API.
type APISpec string

// The search parameters to use for the ELASTIC_SEARCH spec. This data type is not supported
// in Gemini API.
type ExternalAPIElasticSearchParams struct {
	// The ElasticSearch index to use.
	Index string `json:"index,omitempty"`
	// Optional. Number of hits (chunks) to request. When specified, it is passed to Elasticsearch
	// as the `num_hits` param.
	NumHits *int32 `json:"numHits,omitempty"`
	// The ElasticSearch search template to use.
	SearchTemplate string `json:"searchTemplate,omitempty"`
}

// The search parameters to use for SIMPLE_SEARCH spec. This data type is not supported
// in Gemini API.
type ExternalAPISimpleSearchParams struct {
}

// Retrieve from data source powered by external API for grounding. The external API
// is not owned by Google, but need to follow the pre-defined API spec. This data type
// is not supported in Gemini API.
type ExternalAPI struct {
	// The authentication config to access the API. Deprecated. Please use auth_config instead.
	APIAuth *APIAuth `json:"apiAuth,omitempty"`
	// The API spec that the external API implements.
	APISpec APISpec `json:"apiSpec,omitempty"`
	// The authentication config to access the API.
	AuthConfig *AuthConfig `json:"authConfig,omitempty"`
	// Parameters for the elastic search API.
	ElasticSearchParams *ExternalAPIElasticSearchParams `json:"elasticSearchParams,omitempty"`
	// The endpoint of the external API. The system will call the API at this endpoint to
	// retrieve the data for grounding. Example: https://acme.com:443/search
	Endpoint string `json:"endpoint,omitempty"`
	// Parameters for the simple search API.
	SimpleSearchParams *ExternalAPISimpleSearchParams `json:"simpleSearchParams,omitempty"`
}

// Define data stores within engine to filter on in a search call and configurations
// for those data stores. For more information, see https://cloud.google.com/generative-ai-app-builder/docs/reference/rpc/google.cloud.discoveryengine.v1#datastorespec.
// This data type is not supported in Gemini API.
type VertexAISearchDataStoreSpec struct {
	// Full resource name of DataStore, such as Format: `projects/{project}/locations/{location}/collections/{collection}/dataStores/{dataStore}`
	DataStore string `json:"dataStore,omitempty"`
	// Optional. Filter specification to filter documents in the data store specified by
	// data_store field. For more information on filtering, see [Filtering](https://cloud.google.com/generative-ai-app-builder/docs/filter-search-metadata)
	Filter string `json:"filter,omitempty"`
}

// Retrieve from Vertex AI Search datastore or engine for grounding. datastore and engine
// are mutually exclusive. See https://cloud.google.com/products/agent-builder. This
// data type is not supported in Gemini API.
type VertexAISearch struct {
	// Specifications that define the specific DataStores to be searched, along with configurations
	// for those data stores. This is only considered for Engines with multiple data stores.
	// It should only be set if engine is used.
	DataStoreSpecs []*VertexAISearchDataStoreSpec `json:"dataStoreSpecs,omitempty"`
	// Optional. Fully-qualified Vertex AI Search data store resource ID. Format: `projects/{project}/locations/{location}/collections/{collection}/dataStores/{dataStore}`
	Datastore string `json:"datastore,omitempty"`
	// Optional. Fully-qualified Vertex AI Search engine resource ID. Format: `projects/{project}/locations/{location}/collections/{collection}/engines/{engine}`
	Engine string `json:"engine,omitempty"`
	// Optional. Filter strings to be passed to the search API.
	Filter string `json:"filter,omitempty"`
	// Optional. Number of search results to return per query. The default value is 10.
	// The maximumm allowed value is 10.
	MaxResults *int32 `json:"maxResults,omitempty"`
}

// The definition of the RAG resource. This data type is not supported in Gemini API.
type VertexRAGStoreRAGResource struct {
	// Optional. RAGCorpora resource name. Format: `projects/{project}/locations/{location}/ragCorpora/{rag_corpus}`
	RAGCorpus string `json:"ragCorpus,omitempty"`
	// Optional. rag_file_id. The files should be in the same rag_corpus set in rag_corpus
	// field.
	RAGFileIDs []string `json:"ragFileIds,omitempty"`
}

// Config for filters. This data type is not supported in Gemini API.
type RAGRetrievalConfigFilter struct {
	// Optional. String for metadata filtering.
	MetadataFilter string `json:"metadataFilter,omitempty"`
	// Optional. Only returns contexts with vector distance smaller than the threshold.
	VectorDistanceThreshold *float64 `json:"vectorDistanceThreshold,omitempty"`
	// Optional. Only returns contexts with vector similarity larger than the threshold.
	VectorSimilarityThreshold *float64 `json:"vectorSimilarityThreshold,omitempty"`
}

// Config for Hybrid Search. This data type is not supported in Gemini API.
type RAGRetrievalConfigHybridSearch struct {
	// Optional. Alpha value controls the weight between dense and sparse vector search
	// results. The range is [0, 1], while 0 means sparse vector search only and 1 means
	// dense vector search only. The default value is 0.5 which balances sparse and dense
	// vector search equally.
	Alpha *float32 `json:"alpha,omitempty"`
}

// Config for LlmRanker. This data type is not supported in Gemini API.
type RAGRetrievalConfigRankingLlmRanker struct {
	// Optional. The model name used for ranking. See [Supported models](https://cloud.google.com/vertex-ai/generative-ai/docs/model-reference/inference#supported-models).
	ModelName string `json:"modelName,omitempty"`
}

// Config for Rank Service. This data type is not supported in Gemini API.
type RAGRetrievalConfigRankingRankService struct {
	// Optional. The model name of the rank service. Format: `semantic-ranker-512@latest`
	ModelName string `json:"modelName,omitempty"`
}

// Config for ranking and reranking. This data type is not supported in Gemini API.
type RAGRetrievalConfigRanking struct {
	// Optional. Config for LlmRanker.
	LlmRanker *RAGRetrievalConfigRankingLlmRanker `json:"llmRanker,omitempty"`
	// Optional. Config for Rank Service.
	RankService *RAGRetrievalConfigRankingRankService `json:"rankService,omitempty"`
}

// Specifies the context retrieval config. This data type is not supported in Gemini
// API.
type RAGRetrievalConfig struct {
	// Optional. Config for filters.
	Filter *RAGRetrievalConfigFilter `json:"filter,omitempty"`
	// Optional. Config for Hybrid Search.
	HybridSearch *RAGRetrievalConfigHybridSearch `json:"hybridSearch,omitempty"`
	// Optional. Config for ranking and reranking.
	Ranking *RAGRetrievalConfigRanking `json:"ranking,omitempty"`
	// Optional. The number of contexts to retrieve.
	TopK *int32 `json:"topK,omitempty"`
}

// Retrieve from Vertex RAG Store for grounding. This data type is not supported in
// Gemini API. You can find API default values and more details at https://cloud.google.com/vertex-ai/generative-ai/docs/model-reference/rag-api-v1#parameters-list
type VertexRAGStore struct {
	// Optional. Deprecated. Please use rag_resources instead.
	RAGCorpora []string `json:"ragCorpora,omitempty"`
	// Optional. The representation of the RAG source. It can be used to specify corpus
	// only or ragfiles. Currently only support one corpus or multiple files from one corpus.
	// In the future we may open up multiple corpora support.
	RAGResources []*VertexRAGStoreRAGResource `json:"ragResources,omitempty"`
	// Optional. The retrieval config for the RAG query.
	RAGRetrievalConfig *RAGRetrievalConfig `json:"ragRetrievalConfig,omitempty"`
	// Optional. Number of top k results to return from the selected corpora.
	SimilarityTopK *int32 `json:"similarityTopK,omitempty"`
	// Optional. Currently only supported for Gemini Multimodal Live API. In Gemini Multimodal
	// Live API, if `store_context` bool is specified, Gemini will leverage it to automatically
	// memorize the interactions between the client and Gemini, and retrieve context when
	// needed to augment the response generation for users' ongoing and future interactions.
	StoreContext *bool `json:"storeContext,omitempty"`
	// Optional. Only return results with vector distance smaller than the threshold.
	VectorDistanceThreshold *float64 `json:"vectorDistanceThreshold,omitempty"`
}

// Defines a retrieval tool that model can call to access external knowledge. This data
// type is not supported in Gemini API.
type Retrieval struct {
	// Optional. Deprecated. This option is no longer supported.
	DisableAttribution bool `json:"disableAttribution,omitempty"`
	// Use data source powered by external API for grounding.
	ExternalAPI *ExternalAPI `json:"externalApi,omitempty"`
	// Set to use data source powered by Vertex AI Search.
	VertexAISearch *VertexAISearch `json:"vertexAiSearch,omitempty"`
	// Set to use data source powered by Vertex RAG store. User data is uploaded via the
	// VertexRAGDataService.
	VertexRAGStore *VertexRAGStore `json:"vertexRagStore,omitempty"`
}

// The environment being operated.
type Environment string

// Tool to support computer use.
type ComputerUse struct {
	// Optional. Required. The environment being operated.
	Environment Environment `json:"environment,omitempty"`
	// Optional. By default, predefined functions are included in the final model call.
	// Some of them can be explicitly excluded from being automatically included.
	// This can serve two purposes:
	// 1. Using a more restricted / different action space.
	// 2. Improving the definitions / instructions of predefined functions.
	ExcludedPredefinedFunctions []string `json:"excludedPredefinedFunctions,omitempty"`
}

// Tool to retrieve knowledge from the File Search Stores.
type FileSearch struct {
	// Optional. The names of the file_search_stores to retrieve from.
	// Example: `fileSearchStores/my-file-search-store-123`
	FileSearchStoreNames []string `json:"fileSearchStoreNames,omitempty"`
	// Optional. The number of file search retrieval chunks to retrieve.
	TopK *int32 `json:"topK,omitempty"`
	// Optional. Metadata filter to apply to the file search retrieval documents. See https://google.aip.dev/160
	// for the syntax of the filter expression.
	MetadataFilter string `json:"metadataFilter,omitempty"`
}

// Tool that executes code generated by the model, and automatically returns the result
// to the model. See also [ExecutableCode]and [CodeExecutionResult] which are input
// and output to this tool. This data type is not supported in Gemini API.
type ToolCodeExecution struct {
}

// Sites with confidence level chosen & above this value will be blocked from the search
// results. This enum is not supported in Gemini API.
type PhishBlockThreshold string

// Tool to search public web data, powered by Vertex AI Search and Sec4 compliance.
// This data type is not supported in Gemini API.
type EnterpriseWebSearch struct {
	// Optional. List of domains to be excluded from the search results. The default limit
	// is 2000 domains.
	ExcludeDomains []string `json:"excludeDomains,omitempty"`
	// Optional. Sites with confidence level chosen & above this value will be blocked from
	// the search results.
	BlockingConfidence PhishBlockThreshold `json:"blockingConfidence,omitempty"`
}

// Specifies the function Behavior. Currently only supported by the BidiGenerateContent
// method. This enum is not supported in Vertex AI.
type Behavior string

// Structured representation of a function declaration as defined by the [OpenAPI 3.0
// specification](https://spec.openapis.org/oas/v3.0.3). Included in this declaration
// are the function name, description, parameters and response type. This FunctionDeclaration
// is a representation of a block of code that can be used as a `Tool` by the model
// and executed by the client.
type FunctionDeclaration struct {
	// Optional. Description and purpose of the function. Model uses it to decide how and
	// whether to call the function.
	Description string `json:"description,omitempty"`
	// Required. The name of the function to call. Must start with a letter or an underscore.
	// Must be a-z, A-Z, 0-9, or contain underscores, dots and dashes, with a maximum length
	// of 64.
	Name string `json:"name,omitempty"`
	// Optional. Describes the parameters to this function in JSON Schema Object format.
	// Reflects the Open API 3.03 Parameter Object. string Key: the name of the parameter.
	// Parameter names are case sensitive. Schema Value: the Schema defining the type used
	// for the parameter. For function with no parameters, this can be left unset. Parameter
	// names must start with a letter or an underscore and must only contain chars a-z,
	// A-Z, 0-9, or underscores with a maximum length of 64. Example with 1 required and
	// 1 optional parameter: type: OBJECT properties: param1: type: STRING param2: type:
	// INTEGER required: - param1
	Parameters *Schema `json:"parameters,omitempty"`
	// Optional. Describes the parameters to the function in JSON Schema format. The schema
	// must describe an object where the properties are the parameters to the function.
	// For example: ``` { "type": "object", "properties": { "name": { "type": "string" },
	// "age": { "type": "integer" } }, "additionalProperties": false, "required": ["name",
	// "age"], "propertyOrdering": ["name", "age"] } ``` This field is mutually exclusive
	// with `parameters`.
	ParametersJsonSchema any `json:"parametersJsonSchema,omitempty"`
	// Optional. Describes the output from this function in JSON Schema format. Reflects
	// the Open API 3.03 Response Object. The Schema defines the type used for the response
	// value of the function.
	Response *Schema `json:"response,omitempty"`
	// Optional. Describes the output from this function in JSON Schema format. The value
	// specified by the schema is the response value of the function. This field is mutually
	// exclusive with `response`.
	ResponseJsonSchema any `json:"responseJsonSchema,omitempty"`
	// Optional. Specifies the function Behavior. Currently only supported by the BidiGenerateContent
	// method. This field is not supported in Vertex AI.
	Behavior Behavior `json:"behavior,omitempty"`
}

// Tool to retrieve public maps data for grounding, powered by Google.
type GoogleMaps struct {
	// The authentication config to access the API. Only API key is supported. This field
	// is not supported in Gemini API.
	AuthConfig *AuthConfig `json:"authConfig,omitempty"`
	// Optional. If true, include the widget context token in the response.
	EnableWidget *bool `json:"enableWidget,omitempty"`
}

// Represents a time interval, encoded as a Timestamp start (inclusive) and a Timestamp
// end (exclusive). The start must be less than or equal to the end. When the start
// equals the end, the interval is empty (matches no time). When both start and end
// are unspecified, the interval matches any time.
type Interval struct {
	// Optional. Exclusive end of the interval. If specified, a Timestamp matching this
	// interval will have to be before the end.
	EndTime time.Time `json:"endTime,omitempty"`
	// Optional. Inclusive start of the interval. If specified, a Timestamp matching this
	// interval will have to be the same or after the start.
	StartTime time.Time `json:"startTime,omitempty"`
}

func (i *Interval) UnmarshalJSON(data []byte) error {
	type Alias Interval
	aux := &struct {
		EndTime   *time.Time `json:"endTime,omitempty"`
		StartTime *time.Time `json:"startTime,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(i),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	if !reflect.ValueOf(aux.EndTime).IsZero() {
		i.EndTime = time.Time(*aux.EndTime)
	}

	if !reflect.ValueOf(aux.StartTime).IsZero() {
		i.StartTime = time.Time(*aux.StartTime)
	}

	return nil
}

func (i *Interval) MarshalJSON() ([]byte, error) {
	type Alias Interval
	aux := &struct {
		EndTime   *time.Time `json:"endTime,omitempty"`
		StartTime *time.Time `json:"startTime,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(i),
	}

	if !reflect.ValueOf(i.EndTime).IsZero() {
		aux.EndTime = (*time.Time)(&i.EndTime)
	}

	if !reflect.ValueOf(i.StartTime).IsZero() {
		aux.StartTime = (*time.Time)(&i.StartTime)
	}

	return json.Marshal(aux)
}

// GoogleSearch tool type. Tool to support Google Search in Model. Powered by Google.
type GoogleSearch struct {
	// Optional. List of domains to be excluded from the search results. The default limit
	// is 2000 domains. Example: ["amazon.com", "facebook.com"]. This field is not supported
	// in Gemini API.
	ExcludeDomains []string `json:"excludeDomains,omitempty"`
	// Optional. Sites with confidence level chosen & above this value will be blocked from
	// the search results. This field is not supported in Gemini API.
	BlockingConfidence PhishBlockThreshold `json:"blockingConfidence,omitempty"`
	// Optional. Filter search results to a specific time range. If customers set a start
	// time, they must set an end time (and vice versa). This field is not supported in
	// Vertex AI.
	TimeRangeFilter *Interval `json:"timeRangeFilter,omitempty"`
}

// The mode of the predictor to be used in dynamic retrieval.
type DynamicRetrievalConfigMode string

// Describes the options to customize dynamic retrieval.
type DynamicRetrievalConfig struct {
	// Optional. The threshold to be used in dynamic retrieval. If empty, a system default
	// value is used.
	DynamicThreshold *float32 `json:"dynamicThreshold,omitempty"`
	// The mode of the predictor to be used in dynamic retrieval.
	Mode DynamicRetrievalConfigMode `json:"mode,omitempty"`
}

// Tool to retrieve public web data for grounding, powered by Google.
type GoogleSearchRetrieval struct {
	// Specifies the dynamic retrieval configuration for the given source.
	DynamicRetrievalConfig *DynamicRetrievalConfig `json:"dynamicRetrievalConfig,omitempty"`
}

// Tool to support URL context.
type URLContext struct {
}

// Tool details of a tool that the model may use to generate a response.
type Tool struct {
	// Optional. Retrieval tool type. System will always execute the provided retrieval
	// tool(s) to get external knowledge to answer the prompt. Retrieval results are presented
	// to the model for generation. This field is not supported in Gemini API.
	Retrieval *Retrieval `json:"retrieval,omitempty"`
	// Optional. Tool to support the model interacting directly with the
	// computer. If enabled, it automatically populates computer-use specific
	// Function Declarations.
	ComputerUse *ComputerUse `json:"computerUse,omitempty"`
	// Optional. Tool to retrieve knowledge from the File Search Stores.
	FileSearch *FileSearch `json:"fileSearch,omitempty"`
	// Optional. CodeExecution tool type. Enables the model to execute code as part of generation.
	CodeExecution *ToolCodeExecution `json:"codeExecution,omitempty"`
	// Optional. Tool to support searching public web data, powered by Vertex AI Search
	// and Sec4 compliance. This field is not supported in Gemini API.
	EnterpriseWebSearch *EnterpriseWebSearch `json:"enterpriseWebSearch,omitempty"`
	// Optional. Function tool type. One or more function declarations to be passed to the
	// model along with the current user query. Model may decide to call a subset of these
	// functions by populating FunctionCall in the response. User should provide a FunctionResponse
	// for each function call in the next turn. Based on the function responses, Model will
	// generate the final response back to the user. Maximum 512 function declarations can
	// be provided.
	FunctionDeclarations []*FunctionDeclaration `json:"functionDeclarations,omitempty"`
	// Optional. GoogleMaps tool type. Tool to support Google Maps in Model.
	GoogleMaps *GoogleMaps `json:"googleMaps,omitempty"`
	// Optional. GoogleSearch tool type. Tool to support Google Search in Model. Powered
	// by Google.
	GoogleSearch *GoogleSearch `json:"googleSearch,omitempty"`
	// Optional. Specialized retrieval tool that is powered by Google Search.
	GoogleSearchRetrieval *GoogleSearchRetrieval `json:"googleSearchRetrieval,omitempty"`
	// Optional. Tool to support URL context retrieval.
	URLContext *URLContext `json:"urlContext,omitempty"`
}

// An object that represents a latitude/longitude pair.
// This is expressed as a pair of doubles to represent degrees latitude and
// degrees longitude. Unless specified otherwise, this object must conform to the
// <a href="https://en.wikipedia.org/wiki/World_Geodetic_System#1984_version">
// WGS84 standard</a>. Values must be within normalized ranges.
type LatLng struct {
	// Optional. The latitude in degrees. It must be in the range [-90.0, +90.0].
	Latitude *float64 `json:"latitude,omitempty"`
	// Optional. The longitude in degrees. It must be in the range [-180.0, +180.0]
	Longitude *float64 `json:"longitude,omitempty"`
}

// Retrieval config.
type RetrievalConfig struct {
	// Optional. The location of the user.
	LatLng *LatLng `json:"latLng,omitempty"`
	// The language code of the user.
	LanguageCode string `json:"languageCode,omitempty"`
}

// Function calling mode.
type FunctionCallingConfigMode string

// Function calling config.
type FunctionCallingConfig struct {
	// Optional. Function names to call. Only set when the Mode is ANY. Function names should
	// match [FunctionDeclaration.Name]. With mode set to ANY, model will predict a function
	// call from the set of function names provided.
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
	// Optional. Function calling mode.
	Mode FunctionCallingConfigMode `json:"mode,omitempty"`
	// Optional. When set to true, arguments of a single function call will be streamed
	// out in multiple parts/contents/responses. Partial parameter results will be returned
	// in the [FunctionCall.partial_args] field. This field is not supported in Gemini API.
	StreamFunctionCallArguments *bool `json:"streamFunctionCallArguments,omitempty"`
}

// Tool config.
// This config is shared for all tools provided in the request.
type ToolConfig struct {
	// Optional. Retrieval config.
	RetrievalConfig *RetrievalConfig `json:"retrievalConfig,omitempty"`
	// Optional. Function calling config.
	FunctionCallingConfig *FunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

// The media resolution to use.
type MediaResolution string

// ReplicatedVoiceConfig is used to configure replicated voice.
type ReplicatedVoiceConfig struct {
	// Optional. The MIME type of the replicated voice.
	MIMEType string `json:"mimeType,omitempty"`
	// Optional. The sample audio of the replicated voice.
	VoiceSampleAudio []byte `json:"voiceSampleAudio,omitempty"`
}

// The configuration for the prebuilt speaker to use.
type PrebuiltVoiceConfig struct {
	// The name of the preset voice to use.
	VoiceName string `json:"voiceName,omitempty"`
}

// Configuration for a single speaker in a multi speaker setup.
type SpeakerVoiceConfig struct {
	// Required. The name of the speaker. This should be the same as the speaker name used
	// in the prompt.
	Speaker string `json:"speaker,omitempty"`
	// Required. The configuration for the voice of this speaker.
	VoiceConfig *VoiceConfig `json:"voiceConfig,omitempty"`
}

// Configuration for a multi-speaker text-to-speech request.
type MultiSpeakerVoiceConfig struct {
	// Required. A list of configurations for the voices of the speakers. Exactly two speaker
	// voice configurations must be provided.
	SpeakerVoiceConfigs []*SpeakerVoiceConfig `json:"speakerVoiceConfigs,omitempty"`
}

type VoiceConfig struct {
	// Optional. If true, the model will use a replicated voice for the response.
	ReplicatedVoiceConfig *ReplicatedVoiceConfig `json:"replicatedVoiceConfig,omitempty"`
	// The configuration for the prebuilt voice to use.
	PrebuiltVoiceConfig *PrebuiltVoiceConfig `json:"prebuiltVoiceConfig,omitempty"`
}

type SpeechConfig struct {
	// Optional. Configuration for the voice of the response.
	VoiceConfig *VoiceConfig `json:"voiceConfig,omitempty"`
	// Optional. Language code (ISO 639. e.g. en-US) for the speech synthesization.
	LanguageCode string `json:"languageCode,omitempty"`
	// The configuration for a multi-speaker text-to-speech request. This field is mutually
	// exclusive with `voice_config`.
	MultiSpeakerVoiceConfig *MultiSpeakerVoiceConfig `json:"multiSpeakerVoiceConfig,omitempty"`
}

// The number of thoughts tokens that the model should generate.
type ThinkingLevel string

const (
	// Unspecified thinking level.
	ThinkingLevelUnspecified ThinkingLevel = "THINKING_LEVEL_UNSPECIFIED"
	// Low thinking level.
	ThinkingLevelLow ThinkingLevel = "LOW"
	// Medium thinking level.
	ThinkingLevelMedium ThinkingLevel = "MEDIUM"
	// High thinking level.
	ThinkingLevelHigh ThinkingLevel = "HIGH"
	// MINIMAL thinking level.
	ThinkingLevelMinimal ThinkingLevel = "MINIMAL"
)

// The thinking features configuration.
type ThinkingConfig struct {
	// Optional. Indicates whether to include thoughts in the response. If true, thoughts
	// are returned only if the model supports thought and thoughts are available.
	IncludeThoughts bool `json:"includeThoughts,omitempty"`
	// Optional. Indicates the thinking budget in tokens.
	ThinkingBudget *int32 `json:"thinkingBudget,omitempty"`
	// Optional. The number of thoughts tokens that the model should generate.
	ThinkingLevel ThinkingLevel `json:"thinkingLevel,omitempty"`
}

// The image generation configuration to be used in GenerateContentConfig.
type ImageConfig struct {
	// Optional. Aspect ratio of the generated images. Supported values are
	// "1:1", "2:3", "3:2", "3:4", "4:3", "9:16", "16:9", and "21:9".
	AspectRatio string `json:"aspectRatio,omitempty"`
	// Optional. Specifies the size of generated images. Supported
	// values are `1K`, `2K`, `4K`. If not specified, the model will use default
	// value `1K`.
	ImageSize string `json:"imageSize,omitempty"`
	// Optional. Controls the generation of people. Supported values are:
	// ALLOW_ALL, ALLOW_ADULT, ALLOW_NONE.
	PersonGeneration string `json:"personGeneration,omitempty"`
	// Optional. MIME type of the generated image. This field is not
	// supported in Gemini API.
	OutputMIMEType string `json:"outputMimeType,omitempty"`
	// Optional. Compression quality of the generated image (for
	// ``image/jpeg`` only). This field is not supported in Gemini API.
	OutputCompressionQuality *int32 `json:"outputCompressionQuality,omitempty"`
}

// Configuration for Model Armor integrations of prompt and responses. This data type
// is not supported in Gemini API.
type ModelArmorConfig struct {
	// Optional. The name of the Model Armor template to use for prompt sanitization.
	PromptTemplateName string `json:"promptTemplateName,omitempty"`
	// Optional. The name of the Model Armor template to use for response sanitization.
	ResponseTemplateName string `json:"responseTemplateName,omitempty"`
}

// Optional model configuration parameters.
// For more information, see `Content generation parameters
// <https://cloud.google.com/vertex-ai/generative-ai/docs/multimodal/content-generation-parameters>`_.
type GenerateContentConfig struct {
	// Optional. Used to override HTTP request options.
	HTTPOptions *HTTPOptions `json:"httpOptions,omitempty"`
	// Optional. Instructions for the model to steer it toward better performance.
	// For example, "Answer as concisely as possible" or "Don't use technical
	// terms in your response".
	SystemInstruction *Content `json:"systemInstruction,omitempty"`
	// Optional. Value that controls the degree of randomness in token selection.
	// Lower temperatures are good for prompts that require a less open-ended or
	// creative response, while higher temperatures can lead to more diverse or
	// creative results.
	Temperature *float32 `json:"temperature,omitempty"`
	// Optional. Tokens are selected from the most to least probable until the sum
	// of their probabilities equals this value. Use a lower value for less
	// random responses and a higher value for more random responses.
	TopP *float32 `json:"topP,omitempty"`
	// Optional. For each token selection step, the ``top_k`` tokens with the
	// highest probabilities are sampled. Then tokens are further filtered based
	// on ``top_p`` with the final token selected using temperature sampling. Use
	// a lower number for less random responses and a higher number for more
	// random responses.
	TopK *float32 `json:"topK,omitempty"`
	// Optional. Number of response variations to return.
	// If empty, the system will choose a default value (currently 1).
	CandidateCount int32 `json:"candidateCount,omitempty"`
	// Optional. Maximum number of tokens that can be generated in the response.
	// If empty, API will use a default value. The default value varies by model.
	MaxOutputTokens int32 `json:"maxOutputTokens,omitempty"`
	// Optional. List of strings that tells the model to stop generating text if one
	// of the strings is encountered in the response.
	StopSequences []string `json:"stopSequences,omitempty"`
	// Optional. Whether to return the log probabilities of the tokens that were
	// chosen by the model at each step.
	ResponseLogprobs bool `json:"responseLogprobs,omitempty"`
	// Optional. Number of top candidate tokens to return the log probabilities for
	// at each generation step.
	Logprobs *int32 `json:"logprobs,omitempty"`
	// Optional. Positive values penalize tokens that already appear in the
	// generated text, increasing the probability of generating more diverse
	// content.
	PresencePenalty *float32 `json:"presencePenalty,omitempty"`
	// Optional. Positive values penalize tokens that repeatedly appear in the
	// generated text, increasing the probability of generating more diverse
	// content.
	FrequencyPenalty *float32 `json:"frequencyPenalty,omitempty"`
	// Optional. When ``seed`` is fixed to a specific number, the model makes a best
	// effort to provide the same response for repeated requests. By default, a
	// random number is used.
	Seed *int32 `json:"seed,omitempty"`
	// Optional. Output response mimetype of the generated candidate text.
	// Supported mimetype:
	//   - `text/plain`: (default) Text output.
	//   - `application/json`: JSON response in the candidates.
	// The model needs to be prompted to output the appropriate response type,
	// otherwise the behavior is undefined.
	// This is a preview feature.
	ResponseMIMEType string `json:"responseMimeType,omitempty"`
	// Optional. The `Schema` object allows the definition of input and output data types.
	// These types can be objects, but also primitives and arrays.
	// Represents a select subset of an [OpenAPI 3.0 schema
	// object](https://spec.openapis.org/oas/v3.0.3#schema).
	// If set, a compatible response_mime_type must also be set.
	// Compatible mimetypes: `application/json`: Schema for JSON response.
	// If `response_schema` doesn't process your schema correctly, try using
	// `response_json_schema` instead.
	ResponseSchema *Schema `json:"responseSchema,omitempty"`
	// Optional. Output schema of the generated response.
	// This is an alternative to `response_schema` that accepts [JSON
	// Schema](https://json-schema.org/). If set, `response_schema` must be
	// omitted, but `response_mime_type` is required. While the full JSON Schema
	// may be sent, not all features are supported. Specifically, only the
	// following properties are supported: - `$id` - `$defs` - `$ref` - `$anchor`
	//   - `type` - `format` - `title` - `description` - `enum` (for strings and
	// numbers) - `items` - `prefixItems` - `minItems` - `maxItems` - `minimum` -
	// `maximum` - `anyOf` - `oneOf` (interpreted the same as `anyOf`) -
	// `properties` - `additionalProperties` - `required` The non-standard
	// `propertyOrdering` property may also be set. Cyclic references are
	// unrolled to a limited degree and, as such, may only be used within
	// non-required properties. (Nullable properties are not sufficient.) If
	// `$ref` is set on a sub-schema, no other properties, except for than those
	// starting as a `$`, may be set.
	ResponseJsonSchema any `json:"responseJsonSchema,omitempty"`
	// Optional. Configuration for model router requests.
	RoutingConfig *GenerationConfigRoutingConfig `json:"routingConfig,omitempty"`
	// Optional. Configuration for model selection.
	ModelSelectionConfig *ModelSelectionConfig `json:"modelSelectionConfig,omitempty"`
	// Optional. Safety settings in the request to block unsafe content in the
	// response.
	SafetySettings []*SafetySetting `json:"safetySettings,omitempty"`
	// Optional. Code that enables the system to interact with external systems to
	// perform an action outside of the knowledge and scope of the model.
	Tools []*Tool `json:"tools,omitempty"`
	// Optional. Associates model output to a specific function call.
	ToolConfig *ToolConfig `json:"toolConfig,omitempty"`
	// Optional. Labels with user-defined metadata to break down billed charges.
	Labels map[string]string `json:"labels,omitempty"`
	// Optional. Resource name of a context cache that can be used in subsequent
	// requests.
	CachedContent string `json:"cachedContent,omitempty"`
	// Optional. The requested modalities of the response. Represents the set of
	// modalities that the model can return.
	ResponseModalities []string `json:"responseModalities,omitempty"`
	// Optional. If specified, the media resolution specified will be used.
	MediaResolution MediaResolution `json:"mediaResolution,omitempty"`
	// Optional. The speech generation configuration.
	SpeechConfig *SpeechConfig `json:"speechConfig,omitempty"`
	// Optional. If enabled, audio timestamp will be included in the request to the
	// model.
	AudioTimestamp bool `json:"audioTimestamp,omitempty"`
	// Optional. The thinking features configuration.
	ThinkingConfig *ThinkingConfig `json:"thinkingConfig,omitempty"`
	// Optional. The image generation configuration.
	ImageConfig *ImageConfig `json:"imageConfig,omitempty"`
	// Optional. Enables enhanced civic answers. It may not be available for all
	// models. This field is not supported in Vertex AI.
	EnableEnhancedCivicAnswers *bool `json:"enableEnhancedCivicAnswers,omitempty"`
	// Optional. Settings for prompt and response sanitization using the Model Armor
	// service. If supplied, safety_settings must not be supplied.
	ModelArmorConfig *ModelArmorConfig `json:"modelArmorConfig,omitempty"`
}

// Config for inlined request.
type InlinedRequest struct {
	// ID of the model to use. For a list of models, see `Google models
	// <https://cloud.google.com/vertex-ai/generative-ai/docs/learn/models>`_.
	Model string `json:"model,omitempty"`
	// Content of the request.
	Contents []*Content `json:"contents,omitempty"`
	// Optional. The metadata to be associated with the request.
	Metadata map[string]string `json:"metadata,omitempty"`
	// Optional. Configuration that contains optional model parameters.
	Config *GenerateContentConfig `json:"config,omitempty"`
}

// Config for `src` parameter.
type BatchJobSource struct {
	// Storage format of the input files. Must be one of:
	// 'jsonl', 'bigquery'.
	Format string `json:"format,omitempty"`
	// Optional. The Google Cloud Storage URIs to input files.
	GCSURI []string `json:"gcsUri,omitempty"`
	// Optional. The BigQuery URI to input table.
	BigqueryURI string `json:"bigqueryUri,omitempty"`
	// Optional. The Gemini Developer API's file resource name of the input data
	// (e.g. "files/12345").
	FileName string `json:"fileName,omitempty"`
	// Optional. The Gemini Developer API's inlined input data to run batch job.
	InlinedRequests []*InlinedRequest `json:"inlinedRequests,omitempty"`
}

// A wrapper class for the HTTP response.
type HTTPResponse struct {
	// Optional. Used to retain the processed HTTP headers in the response.
	Headers http.Header `json:"headers,omitempty"`
	// Optional. The raw HTTP response body, in JSON format.
	Body string `json:"body,omitempty"`
}

type dateJSON civil.Date

func (d *dateJSON) UnmarshalJSON(data []byte) error {
	m := make(map[string]int)
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("failed to unmarshal date from map: %w", err)
	}

	if len(m) == 0 {
		return nil
	}
	if _, ok := m["year"]; !ok {
		return fmt.Errorf("key %q not found", "year")
	}
	d.Year = m["year"]

	if month, ok := m["month"]; ok {
		d.Month = time.Month(month)
	}
	if day, ok := m["day"]; ok {
		d.Day = day
	}
	return nil
}

func (d *dateJSON) MarshalJSON() ([]byte, error) {
	m := make(map[string]int)
	if d == nil || (civil.Date)(*d).IsZero() {
		return json.Marshal(nil)
	}
	if d.Year != 0 {
		m["year"] = d.Year
	}
	if d.Month != 0 {
		m["month"] = int(d.Month)
	}
	if d.Day != 0 {
		m["day"] = d.Day
	}
	return json.Marshal(m)
}

// Source attributions for content. This data type is not supported in Gemini API.
type Citation struct {
	// Output only. End index into the content.
	EndIndex int32 `json:"endIndex,omitempty"`
	// Output only. License of the attribution.
	License string `json:"license,omitempty"`
	// Output only. Publication date of the attribution.
	PublicationDate civil.Date `json:"publicationDate,omitempty"`
	// Output only. Start index into the content.
	StartIndex int32 `json:"startIndex,omitempty"`
	// Output only. Title of the attribution.
	Title string `json:"title,omitempty"`
	// Output only. URL reference of the attribution.
	URI string `json:"uri,omitempty"`
}

func (c *Citation) UnmarshalJSON(data []byte) error {
	type Alias Citation
	aux := &struct {
		PublicationDate *dateJSON `json:"publicationDate,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(c),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	if !reflect.ValueOf(aux.PublicationDate).IsZero() {
		c.PublicationDate = civil.Date(*aux.PublicationDate)
	}

	return nil
}

func (c *Citation) MarshalJSON() ([]byte, error) {
	type Alias Citation
	aux := &struct {
		PublicationDate *dateJSON `json:"publicationDate,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(c),
	}

	if !reflect.ValueOf(c.PublicationDate).IsZero() {
		aux.PublicationDate = (*dateJSON)(&c.PublicationDate)
	}

	return json.Marshal(aux)
}

// Citation information when the model quotes another source.
type CitationMetadata struct {
	// Optional. Contains citation information when the model directly quotes, at
	// length, from another source. Can include traditional websites and code
	// repositories.
	Citations []*Citation `json:"citations,omitempty"`
}

// The reason why the model stopped generating tokens.
// If empty, the model has not stopped generating the tokens.
type FinishReason string

const (
	// The finish reason is unspecified.
	FinishReasonUnspecified FinishReason = "FINISH_REASON_UNSPECIFIED"
	// Token generation reached a natural stopping point or a configured stop sequence.
	FinishReasonStop FinishReason = "STOP"
	// Token generation reached the configured maximum output tokens.
	FinishReasonMaxTokens FinishReason = "MAX_TOKENS"
	// Token generation stopped because the content potentially contains safety violations.
	// NOTE: When streaming, [content][] is empty if content filters blocks the output.
	FinishReasonSafety FinishReason = "SAFETY"
	// The token generation stopped because of potential recitation.
	FinishReasonRecitation FinishReason = "RECITATION"
	// The token generation stopped because of using an unsupported language.
	FinishReasonLanguage FinishReason = "LANGUAGE"
	// All other reasons that stopped the token generation.
	FinishReasonOther FinishReason = "OTHER"
	// Token generation stopped because the content contains forbidden terms.
	FinishReasonBlocklist FinishReason = "BLOCKLIST"
	// Token generation stopped for potentially containing prohibited content.
	FinishReasonProhibitedContent FinishReason = "PROHIBITED_CONTENT"
	// Token generation stopped because the content potentially contains Sensitive Personally
	// Identifiable Information (SPII).
	FinishReasonSPII FinishReason = "SPII"
	// The function call generated by the model is invalid.
	FinishReasonMalformedFunctionCall FinishReason = "MALFORMED_FUNCTION_CALL"
	// Token generation stopped because generated images have safety violations.
	FinishReasonImageSafety FinishReason = "IMAGE_SAFETY"
	// The tool call generated by the model is invalid.
	FinishReasonUnexpectedToolCall FinishReason = "UNEXPECTED_TOOL_CALL"
	// Image generation stopped because the generated images have prohibited content.
	FinishReasonImageProhibitedContent FinishReason = "IMAGE_PROHIBITED_CONTENT"
	// The model was expected to generate an image, but none was generated.
	FinishReasonNoImage FinishReason = "NO_IMAGE"
	// Image generation stopped because the generated image may be a recitation from a source.
	FinishReasonImageRecitation FinishReason = "IMAGE_RECITATION"
	// Image generation stopped for a reason not otherwise specified.
	FinishReasonImageOther FinishReason = "IMAGE_OTHER"
)

// Author attribution for a photo or review. This data type is not supported in Gemini
// API.
type GroundingChunkMapsPlaceAnswerSourcesAuthorAttribution struct {
	// Name of the author of the Photo or Review.
	DisplayName string `json:"displayName,omitempty"`
	// Profile photo URI of the author of the Photo or Review.
	PhotoURI string `json:"photoUri,omitempty"`
	// URI of the author of the Photo or Review.
	URI string `json:"uri,omitempty"`
}

// Encapsulates a review snippet. This data type is not supported in Gemini API.
type GroundingChunkMapsPlaceAnswerSourcesReviewSnippet struct {
	// This review's author.
	AuthorAttribution *GroundingChunkMapsPlaceAnswerSourcesAuthorAttribution `json:"authorAttribution,omitempty"`
	// A link where users can flag a problem with the review.
	FlagContentURI string `json:"flagContentUri,omitempty"`
	// A link to show the review on Google Maps.
	GoogleMapsURI string `json:"googleMapsUri,omitempty"`
	// A string of formatted recent time, expressing the review time relative to the current
	// time in a form appropriate for the language and country.
	RelativePublishTimeDescription string `json:"relativePublishTimeDescription,omitempty"`
	// A reference representing this place review which may be used to look up this place
	// review again.
	Review string `json:"review,omitempty"`
	// ID of the review referencing the place.
	ReviewID string `json:"reviewId,omitempty"`
	// Title of the review.
	Title string `json:"title,omitempty"`
}

// Sources used to generate the place answer. This data type is not supported in Gemini
// API.
type GroundingChunkMapsPlaceAnswerSources struct {
	// A link where users can flag a problem with the generated answer.
	FlagContentURI string `json:"flagContentUri,omitempty"`
	// Snippets of reviews that are used to generate the answer.
	ReviewSnippets []*GroundingChunkMapsPlaceAnswerSourcesReviewSnippet `json:"reviewSnippets,omitempty"`
}

// Chunk from Google Maps. This data type is not supported in Gemini API.
type GroundingChunkMaps struct {
	// Sources used to generate the place answer. This includes review snippets and photos
	// that were used to generate the answer, as well as uris to flag content.
	PlaceAnswerSources *GroundingChunkMapsPlaceAnswerSources `json:"placeAnswerSources,omitempty"`
	// This Place's resource name, in `places/{place_id}` format. Can be used to look up
	// the Place.
	PlaceID string `json:"placeId,omitempty"`
	// Text of the place answer.
	Text string `json:"text,omitempty"`
	// Title of the place.
	Title string `json:"title,omitempty"`
	// URI reference of the place.
	URI string `json:"uri,omitempty"`
}

// Represents where the chunk starts and ends in the document. This data type is not
// supported in Gemini API.
type RAGChunkPageSpan struct {
	// Page where chunk starts in the document. Inclusive. 1-indexed.
	FirstPage int32 `json:"firstPage,omitempty"`
	// Page where chunk ends in the document. Inclusive. 1-indexed.
	LastPage int32 `json:"lastPage,omitempty"`
}

// A RAGChunk includes the content of a chunk of a RAGFile, and associated metadata.
// This data type is not supported in Gemini API.
type RAGChunk struct {
	// If populated, represents where the chunk starts and ends in the document.
	PageSpan *RAGChunkPageSpan `json:"pageSpan,omitempty"`
	// The content of the chunk.
	Text string `json:"text,omitempty"`
}

// Chunk from context retrieved by the retrieval tools. This data type is not supported
// in Gemini API.
type GroundingChunkRetrievedContext struct {
	// Output only. The full document name for the referenced Vertex AI Search document.
	DocumentName string `json:"documentName,omitempty"`
	// Additional context for the RAG retrieval result. This is only populated when using
	// the RAG retrieval tool.
	RAGChunk *RAGChunk `json:"ragChunk,omitempty"`
	// Text of the attribution.
	Text string `json:"text,omitempty"`
	// Title of the attribution.
	Title string `json:"title,omitempty"`
	// URI reference of the attribution.
	URI string `json:"uri,omitempty"`
}

// Chunk from the web.
type GroundingChunkWeb struct {
	// Domain of the (original) URI. This field is not supported in Gemini API.
	Domain string `json:"domain,omitempty"`
	// Title of the chunk.
	Title string `json:"title,omitempty"`
	// URI reference of the chunk.
	URI string `json:"uri,omitempty"`
}

// Grounding chunk.
type GroundingChunk struct {
	// Grounding chunk from Google Maps. This field is not supported in Gemini API.
	Maps *GroundingChunkMaps `json:"maps,omitempty"`
	// Grounding chunk from context retrieved by the retrieval tools. This field is not
	// supported in Gemini API.
	RetrievedContext *GroundingChunkRetrievedContext `json:"retrievedContext,omitempty"`
	// Grounding chunk from the web.
	Web *GroundingChunkWeb `json:"web,omitempty"`
}

// Segment of the content.
type Segment struct {
	// Output only. End index in the given Part, measured in bytes. Offset from the start
	// of the Part, exclusive, starting at zero.
	EndIndex int32 `json:"endIndex,omitempty"`
	// Output only. The index of a Part object within its parent Content object.
	PartIndex int32 `json:"partIndex,omitempty"`
	// Output only. Start index in the given Part, measured in bytes. Offset from the start
	// of the Part, inclusive, starting at zero.
	StartIndex int32 `json:"startIndex,omitempty"`
	// Output only. The text corresponding to the segment from the response.
	Text string `json:"text,omitempty"`
}

// Grounding support.
type GroundingSupport struct {
	// Confidence score of the support references. Ranges from 0 to 1. 1 is the most confident.
	// For Gemini 2.0 and before, this list must have the same size as the grounding_chunk_indices.
	// For Gemini 2.5 and after, this list will be empty and should be ignored.
	ConfidenceScores []float32 `json:"confidenceScores,omitempty"`
	// A list of indices (into 'grounding_chunk') specifying the citations associated with
	// the claim. For instance [1,3,4] means that grounding_chunk[1], grounding_chunk[3],
	// grounding_chunk[4] are the retrieved content attributed to the claim.
	GroundingChunkIndices []int32 `json:"groundingChunkIndices,omitempty"`
	// Segment of the content this support belongs to.
	Segment *Segment `json:"segment,omitempty"`
}

// Metadata related to retrieval in the grounding flow.
type RetrievalMetadata struct {
	// Optional. Score indicating how likely information from Google Search could help answer
	// the prompt. The score is in the range `[0, 1]`, where 0 is the least likely and 1
	// is the most likely. This score is only populated when Google Search grounding and
	// dynamic retrieval is enabled. It will be compared to the threshold to determine whether
	// to trigger Google Search.
	GoogleSearchDynamicRetrievalScore float32 `json:"googleSearchDynamicRetrievalScore,omitempty"`
}

// Google search entry point.
type SearchEntryPoint struct {
	// Optional. Web content snippet that can be embedded in a web page or an app webview.
	RenderedContent string `json:"renderedContent,omitempty"`
	// Optional. Base64 encoded JSON representing array of tuple.
	SDKBlob []byte `json:"sdkBlob,omitempty"`
}

// Source content flagging URI for a place or review. This is currently populated only
// for Google Maps grounding. This data type is not supported in Gemini API.
type GroundingMetadataSourceFlaggingURI struct {
	// A link where users can flag a problem with the source (place or review).
	FlagContentURI string `json:"flagContentUri,omitempty"`
	// ID of the place or review.
	SourceID string `json:"sourceId,omitempty"`
}

// Metadata returned to client when grounding is enabled.
type GroundingMetadata struct {
	// Optional. Output only. Resource name of the Google Maps widget context token to be
	// used with the PlacesContextElement widget to render contextual data. This is populated
	// only for Google Maps grounding. This field is not supported in Gemini API.
	GoogleMapsWidgetContextToken string `json:"googleMapsWidgetContextToken,omitempty"`
	// List of supporting references retrieved from specified grounding source.
	GroundingChunks []*GroundingChunk `json:"groundingChunks,omitempty"`
	// Optional. List of grounding support.
	GroundingSupports []*GroundingSupport `json:"groundingSupports,omitempty"`
	// Optional. Output only. Retrieval metadata.
	RetrievalMetadata *RetrievalMetadata `json:"retrievalMetadata,omitempty"`
	// Optional. Queries executed by the retrieval tools. This field is not supported in
	// Gemini API.
	RetrievalQueries []string `json:"retrievalQueries,omitempty"`
	// Optional. Google search entry for the following-up web searches.
	SearchEntryPoint *SearchEntryPoint `json:"searchEntryPoint,omitempty"`
	// Optional. Output only. List of source flagging uris. This is currently populated
	// only for Google Maps grounding. This field is not supported in Gemini API.
	SourceFlaggingUris []*GroundingMetadataSourceFlaggingURI `json:"sourceFlaggingUris,omitempty"`
	// Optional. Web search queries for the following-up web search.
	WebSearchQueries []string `json:"webSearchQueries,omitempty"`
}

// Candidate for the logprobs token and score.
type LogprobsResultCandidate struct {
	// The candidate's log probability.
	LogProbability float32 `json:"logProbability,omitempty"`
	// The candidate's token string value.
	Token string `json:"token,omitempty"`
	// The candidate's token ID value.
	TokenID int32 `json:"tokenId,omitempty"`
}

// Candidates with top log probabilities at each decoding step.
type LogprobsResultTopCandidates struct {
	// Sorted by log probability in descending order.
	Candidates []*LogprobsResultCandidate `json:"candidates,omitempty"`
}

// Logprobs Result
type LogprobsResult struct {
	// Length = total number of decoding steps. The chosen candidates may or may not be
	// in top_candidates.
	ChosenCandidates []*LogprobsResultCandidate `json:"chosenCandidates,omitempty"`
	// Length = total number of decoding steps.
	TopCandidates []*LogprobsResultTopCandidates `json:"topCandidates,omitempty"`
}

// Harm probability levels in the content.
type HarmProbability string

const (
	// Harm probability unspecified.
	HarmProbabilityUnspecified HarmProbability = "HARM_PROBABILITY_UNSPECIFIED"
	// Negligible level of harm.
	HarmProbabilityNegligible HarmProbability = "NEGLIGIBLE"
	// Low level of harm.
	HarmProbabilityLow HarmProbability = "LOW"
	// Medium level of harm.
	HarmProbabilityMedium HarmProbability = "MEDIUM"
	// High level of harm.
	HarmProbabilityHigh HarmProbability = "HIGH"
)

// Harm severity levels in the content. This enum is not supported in Gemini API.
type HarmSeverity string

const (
	// Harm severity unspecified.
	HarmSeverityUnspecified HarmSeverity = "HARM_SEVERITY_UNSPECIFIED"
	// Negligible level of harm severity.
	HarmSeverityNegligible HarmSeverity = "HARM_SEVERITY_NEGLIGIBLE"
	// Low level of harm severity.
	HarmSeverityLow HarmSeverity = "HARM_SEVERITY_LOW"
	// Medium level of harm severity.
	HarmSeverityMedium HarmSeverity = "HARM_SEVERITY_MEDIUM"
	// High level of harm severity.
	HarmSeverityHigh HarmSeverity = "HARM_SEVERITY_HIGH"
)

// Safety rating corresponding to the generated content.
type SafetyRating struct {
	// Output only. Indicates whether the content was filtered out because of this rating.
	Blocked bool `json:"blocked,omitempty"`
	// Output only. Harm category.
	Category HarmCategory `json:"category,omitempty"`
	// Output only. The overwritten threshold for the safety category of Gemini 2.0 image
	// out. If minors are detected in the output image, the threshold of each safety category
	// will be overwritten if user sets a lower threshold. This field is not supported in
	// Gemini API.
	OverwrittenThreshold HarmBlockThreshold `json:"overwrittenThreshold,omitempty"`
	// Output only. Harm probability levels in the content.
	Probability HarmProbability `json:"probability,omitempty"`
	// Output only. Harm probability score. This field is not supported in Gemini API.
	ProbabilityScore float32 `json:"probabilityScore,omitempty"`
	// Output only. Harm severity levels in the content. This field is not supported in
	// Gemini API.
	Severity HarmSeverity `json:"severity,omitempty"`
	// Output only. Harm severity score. This field is not supported in Gemini API.
	SeverityScore float32 `json:"severityScore,omitempty"`
}

// Status of the URL retrieval.
type URLRetrievalStatus string

const (
	// Default value. This value is unused.
	URLRetrievalStatusUnspecified URLRetrievalStatus = "URL_RETRIEVAL_STATUS_UNSPECIFIED"
	// URL retrieval is successful.
	URLRetrievalStatusSuccess URLRetrievalStatus = "URL_RETRIEVAL_STATUS_SUCCESS"
	// URL retrieval is failed due to error.
	URLRetrievalStatusError URLRetrievalStatus = "URL_RETRIEVAL_STATUS_ERROR"
	// URL retrieval is failed because the content is behind paywall. This enum value is
	// not supported in Vertex AI.
	URLRetrievalStatusPaywall URLRetrievalStatus = "URL_RETRIEVAL_STATUS_PAYWALL"
	// URL retrieval is failed because the content is unsafe. This enum value is not supported
	// in Vertex AI.
	URLRetrievalStatusUnsafe URLRetrievalStatus = "URL_RETRIEVAL_STATUS_UNSAFE"
)

// Context of the a single URL retrieval.
type URLMetadata struct {
	// Retrieved URL by the tool.
	RetrievedURL string `json:"retrievedUrl,omitempty"`
	// Status of the URL retrieval.
	URLRetrievalStatus URLRetrievalStatus `json:"urlRetrievalStatus,omitempty"`
}

// Metadata related to URL context retrieval tool.
type URLContextMetadata struct {
	// Output only. List of URL context.
	URLMetadata []*URLMetadata `json:"urlMetadata,omitempty"`
}

// A response candidate generated from the model.
type Candidate struct {
	// Optional. Contains the multi-part content of the response.
	Content *Content `json:"content,omitempty"`
	// Optional. Source attribution of the generated content.
	CitationMetadata *CitationMetadata `json:"citationMetadata,omitempty"`
	// Optional. Describes the reason the model stopped generating tokens.
	FinishMessage string `json:"finishMessage,omitempty"`
	// Optional. Number of tokens for this candidate.
	// This field is only available in the Gemini API.
	TokenCount int32 `json:"tokenCount,omitempty"`
	// Optional. The reason why the model stopped generating tokens.
	// If empty, the model has not stopped generating the tokens.
	FinishReason FinishReason `json:"finishReason,omitempty"`
	// Output only. Average log probability score of the candidate.
	AvgLogprobs float64 `json:"avgLogprobs,omitempty"`
	// Output only. Metadata specifies sources used to ground generated content.
	GroundingMetadata *GroundingMetadata `json:"groundingMetadata,omitempty"`
	// Output only. Index of the candidate.
	Index int32 `json:"index,omitempty"`
	// Output only. Log-likelihood scores for the response tokens and top tokens
	LogprobsResult *LogprobsResult `json:"logprobsResult,omitempty"`
	// Output only. List of ratings for the safety of a response candidate. There is at
	// most one rating per category.
	SafetyRatings []*SafetyRating `json:"safetyRatings,omitempty"`
	// Output only. Metadata related to URL context retrieval tool.
	URLContextMetadata *URLContextMetadata `json:"urlContextMetadata,omitempty"`
}

// The reason why the prompt was blocked.
type BlockedReason string

const (
	// The blocked reason is unspecified.
	BlockedReasonUnspecified BlockedReason = "BLOCKED_REASON_UNSPECIFIED"
	// The prompt was blocked for safety reasons.
	BlockedReasonSafety BlockedReason = "SAFETY"
	// The prompt was blocked for other reasons. For example, it may be due to the prompt's
	// language, or because it contains other harmful content.
	BlockedReasonOther BlockedReason = "OTHER"
	// The prompt was blocked because it contains a term from the terminology blocklist.
	BlockedReasonBlocklist BlockedReason = "BLOCKLIST"
	// The prompt was blocked because it contains prohibited content.
	BlockedReasonProhibitedContent BlockedReason = "PROHIBITED_CONTENT"
	// The prompt was blocked because it contains content that is unsafe for image generation.
	BlockedReasonImageSafety BlockedReason = "IMAGE_SAFETY"
	// The prompt was blocked by Model Armor. This enum value is not supported in Gemini
	// API.
	BlockedReasonModelArmor BlockedReason = "MODEL_ARMOR"
	// The prompt was blocked as a jailbreak attempt. This enum value is not supported in
	// Gemini API.
	BlockedReasonJailbreak BlockedReason = "JAILBREAK"
)

// Content filter results for a prompt sent in the request. Note: This is sent only
// in the first stream chunk and only if no candidates were generated due to content
// violations.
type GenerateContentResponsePromptFeedback struct {
	// Output only. The reason why the prompt was blocked.
	BlockReason BlockedReason `json:"blockReason,omitempty"`
	// Output only. A readable message that explains the reason why the prompt was blocked.
	// This field is not supported in Gemini API.
	BlockReasonMessage string `json:"blockReasonMessage,omitempty"`
	// Output only. A list of safety ratings for the prompt. There is one rating per category.
	SafetyRatings []*SafetyRating `json:"safetyRatings,omitempty"`
}

// Server content modalities.
type MediaModality string

const (
	// The modality is unspecified.
	MediaModalityUnspecified MediaModality = "MODALITY_UNSPECIFIED"
	// Plain text.
	MediaModalityText MediaModality = "TEXT"
	// Images.
	MediaModalityImage MediaModality = "IMAGE"
	// Video.
	MediaModalityVideo MediaModality = "VIDEO"
	// Audio.
	MediaModalityAudio MediaModality = "AUDIO"
	// Document, e.g. PDF.
	MediaModalityDocument MediaModality = "DOCUMENT"
)

// Represents token counting info for a single modality.
type ModalityTokenCount struct {
	// Optional. The modality associated with this token count.
	Modality MediaModality `json:"modality,omitempty"`
	// Number of tokens.
	TokenCount int32 `json:"tokenCount,omitempty"`
}

// The traffic type for this request. This enum is not supported in Gemini API.
type TrafficType string

const (
	// Unspecified request traffic type.
	TrafficTypeUnspecified TrafficType = "TRAFFIC_TYPE_UNSPECIFIED"
	// The request was processed using Pay-As-You-Go quota.
	TrafficTypeOnDemand TrafficType = "ON_DEMAND"
	// Type for Provisioned Throughput traffic.
	TrafficTypeProvisionedThroughput TrafficType = "PROVISIONED_THROUGHPUT"
)

// Usage metadata about the content generation request and response. This message provides
// a detailed breakdown of token usage and other relevant metrics. This data type is
// not supported in Gemini API.
type GenerateContentResponseUsageMetadata struct {
	// Output only. A detailed breakdown of the token count for each modality in the cached
	// content.
	CacheTokensDetails []*ModalityTokenCount `json:"cacheTokensDetails,omitempty"`
	// Output only. The number of tokens in the cached content that was used for this request.
	CachedContentTokenCount int32 `json:"cachedContentTokenCount,omitempty"`
	// The total number of tokens in the generated candidates. This includes all the generated
	// response candidates.
	CandidatesTokenCount int32 `json:"candidatesTokenCount,omitempty"`
	// Output only. A detailed breakdown of the token count for each modality in the generated
	// candidates.
	CandidatesTokensDetails []*ModalityTokenCount `json:"candidatesTokensDetails,omitempty"`
	// Number of tokens in the prompt. When cached_content is set, this is still the total
	// effective prompt size meaning this includes the number of tokens in the cached content.
	PromptTokenCount int32 `json:"promptTokenCount,omitempty"`
	// Output only. A detailed breakdown of the token count for each modality in the prompt.
	PromptTokensDetails []*ModalityTokenCount `json:"promptTokensDetails,omitempty"`
	// Output only. The number of tokens that were part of the model's generated "thoughts"
	// output, if applicable.
	ThoughtsTokenCount int32 `json:"thoughtsTokenCount,omitempty"`
	// Output only. The number of tokens in the results from tool executions, which are
	// provided back to the model as input, if applicable.
	ToolUsePromptTokenCount int32 `json:"toolUsePromptTokenCount,omitempty"`
	// Output only. A detailed breakdown by modality of the token counts from the results
	// of tool executions, which are provided back to the model as input.
	ToolUsePromptTokensDetails []*ModalityTokenCount `json:"toolUsePromptTokensDetails,omitempty"`
	// The total number of tokens for the entire request. This is the sum of `prompt_token_count`,
	// `candidates_token_count`, `tool_use_prompt_token_count`, and `thoughts_token_count`.
	TotalTokenCount int32 `json:"totalTokenCount,omitempty"`
	// Output only. The traffic type for this request.
	TrafficType TrafficType `json:"trafficType,omitempty"`
}

// Response message for PredictionService.GenerateContent.
type GenerateContentResponse struct {
	// Optional. Used to retain the full HTTP response.
	SDKHTTPResponse *HTTPResponse `json:"sdkHttpResponse,omitempty"`
	// Response variations returned by the model.
	Candidates []*Candidate `json:"candidates,omitempty"`
	// Timestamp when the request is made to the server.
	CreateTime time.Time `json:"createTime,omitempty"`
	// Output only. The model version used to generate the response.
	ModelVersion string `json:"modelVersion,omitempty"`
	// Output only. Content filter results for a prompt sent in the request. Note: Sent
	// only in the first stream chunk. Only happens when no candidates were generated due
	// to content violations.
	PromptFeedback *GenerateContentResponsePromptFeedback `json:"promptFeedback,omitempty"`
	// Output only. response_id is used to identify each response. It is the encoding of
	// the event_id.
	ResponseID string `json:"responseId,omitempty"`
	// Usage metadata about the response(s).
	UsageMetadata *GenerateContentResponseUsageMetadata `json:"usageMetadata,omitempty"`
}

func (g *GenerateContentResponse) UnmarshalJSON(data []byte) error {
	type Alias GenerateContentResponse
	aux := &struct {
		CreateTime *time.Time `json:"createTime,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(g),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	if !reflect.ValueOf(aux.CreateTime).IsZero() {
		g.CreateTime = time.Time(*aux.CreateTime)
	}

	return nil
}

func (g *GenerateContentResponse) MarshalJSON() ([]byte, error) {
	type Alias GenerateContentResponse
	aux := &struct {
		CreateTime *time.Time `json:"createTime,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(g),
	}

	if !reflect.ValueOf(g.CreateTime).IsZero() {
		aux.CreateTime = (*time.Time)(&g.CreateTime)
	}

	return json.Marshal(aux)
}

// Config for `inlined_responses` parameter.
type InlinedResponse struct {
	// The response to the request.
	Response *GenerateContentResponse `json:"response,omitempty"`
	// Optional. The metadata to be associated with the request.
	Metadata map[string]string `json:"metadata,omitempty"`
	// Optional. The error encountered while processing the request.
	Error *JobError `json:"error,omitempty"`
}

// Config for `response` parameter.
type SingleEmbedContentResponse struct {
	// The response to the request.
	Embedding *ContentEmbedding `json:"embedding,omitempty"`
	// Optional. The error encountered while processing the request.
	TokenCount int64 `json:"tokenCount,omitempty,string"`
}

// Config for `inlined_embedding_responses` parameter.
type InlinedEmbedContentResponse struct {
	// The response to the request.
	Response *SingleEmbedContentResponse `json:"response,omitempty"`
	// Optional. The error encountered while processing the request.
	Error *JobError `json:"error,omitempty"`
}

// Config for `des` parameter.
type BatchJobDestination struct {
	// Storage format of the output files. Must be one of:
	// 'jsonl', 'bigquery'.
	Format string `json:"format,omitempty"`
	// Optional. The Google Cloud Storage URI to the output file.
	GCSURI string `json:"gcsUri,omitempty"`
	// Optional. The BigQuery URI to the output table.
	BigqueryURI string `json:"bigqueryUri,omitempty"`
	// Optional. The Gemini Developer API's file resource name of the output data
	// (e.g. "files/12345"). The file will be a JSONL file with a single response
	// per line. The responses will be GenerateContentResponse messages formatted
	// as JSON. The responses will be written in the same order as the input
	// requests.
	FileName string `json:"fileName,omitempty"`
	// Optional. The responses to the requests in the batch. Returned when the batch was
	// built using inlined requests. The responses will be in the same order as
	// the input requests.
	InlinedResponses []*InlinedResponse `json:"inlinedResponses,omitempty"`
	// Optional. The responses to the requests in the batch. Returned when the batch was
	// built using inlined requests. The responses will be in the same order as
	// the input requests.
	InlinedEmbedContentResponses []*InlinedEmbedContentResponse `json:"inlinedEmbedContentResponses,omitempty"`
}

// Success and error statistics of processing multiple entities (for example, DataItems
// or structured data rows) in batch. This data type is not supported in Gemini API.
type CompletionStats struct {
	// Output only. The number of entities for which any error was encountered.
	FailedCount int64 `json:"failedCount,omitempty,string"`
	// Output only. In cases when enough errors are encountered a job, pipeline, or operation
	// may be failed as a whole. Below is the number of entities for which the processing
	// had not been finished (either in successful or failed state). Set to -1 if the number
	// is unknown (for example, the operation failed before the total entity number could
	// be collected).
	IncompleteCount int64 `json:"incompleteCount,omitempty,string"`
	// Output only. The number of entities that had been processed successfully.
	SuccessfulCount int64 `json:"successfulCount,omitempty,string"`
	// Output only. The number of the successful forecast points that are generated by the
	// forecasting model. This is ONLY used by the forecasting batch prediction.
	SuccessfulForecastPointCount int64 `json:"successfulForecastPointCount,omitempty,string"`
}

// Config for batches.create return value.
type BatchJob struct {
	// The resource name of the BatchJob. Output only.".
	Name string `json:"name,omitempty"`
	// The display name of the BatchJob.
	DisplayName string `json:"displayName,omitempty"`
	// The state of the BatchJob.
	State JobState `json:"state,omitempty"`
	// Output only. Only populated when the job's state is JOB_STATE_FAILED or JOB_STATE_CANCELLED.
	Error *JobError `json:"error,omitempty"`
	// The time when the BatchJob was created.
	CreateTime time.Time `json:"createTime,omitempty"`
	// Output only. Time when the Job for the first time entered the `JOB_STATE_RUNNING`
	// state.
	StartTime time.Time `json:"startTime,omitempty"`
	// The time when the BatchJob was completed. This field is for Vertex AI only.
	EndTime time.Time `json:"endTime,omitempty"`
	// The time when the BatchJob was last updated.
	UpdateTime time.Time `json:"updateTime,omitempty"`
	// The name of the model that produces the predictions via the BatchJob.
	Model string `json:"model,omitempty"`
	// Configuration for the input data. This field is for Vertex AI only.
	Src *BatchJobSource `json:"src,omitempty"`
	// Configuration for the output data.
	Dest *BatchJobDestination `json:"dest,omitempty"`
	// Statistics on completed and failed prediction instances. This field is for Vertex
	// AI only.
	CompletionStats *CompletionStats `json:"completionStats,omitempty"`
}

func (b *BatchJob) UnmarshalJSON(data []byte) error {
	type Alias BatchJob
	aux := &struct {
		CreateTime *time.Time `json:"createTime,omitempty"`
		StartTime  *time.Time `json:"startTime,omitempty"`
		EndTime    *time.Time `json:"endTime,omitempty"`
		UpdateTime *time.Time `json:"updateTime,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(b),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	if !reflect.ValueOf(aux.CreateTime).IsZero() {
		b.CreateTime = time.Time(*aux.CreateTime)
	}

	if !reflect.ValueOf(aux.StartTime).IsZero() {
		b.StartTime = time.Time(*aux.StartTime)
	}

	if !reflect.ValueOf(aux.EndTime).IsZero() {
		b.EndTime = time.Time(*aux.EndTime)
	}

	if !reflect.ValueOf(aux.UpdateTime).IsZero() {
		b.UpdateTime = time.Time(*aux.UpdateTime)
	}

	return nil
}

func (b *BatchJob) MarshalJSON() ([]byte, error) {
	type Alias BatchJob
	aux := &struct {
		CreateTime *time.Time `json:"createTime,omitempty"`
		StartTime  *time.Time `json:"startTime,omitempty"`
		EndTime    *time.Time `json:"endTime,omitempty"`
		UpdateTime *time.Time `json:"updateTime,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(b),
	}

	if !reflect.ValueOf(b.CreateTime).IsZero() {
		aux.CreateTime = (*time.Time)(&b.CreateTime)
	}

	if !reflect.ValueOf(b.StartTime).IsZero() {
		aux.StartTime = (*time.Time)(&b.StartTime)
	}

	if !reflect.ValueOf(b.EndTime).IsZero() {
		aux.EndTime = (*time.Time)(&b.EndTime)
	}

	if !reflect.ValueOf(b.UpdateTime).IsZero() {
		aux.UpdateTime = (*time.Time)(&b.UpdateTime)
	}

	return json.Marshal(aux)
}
