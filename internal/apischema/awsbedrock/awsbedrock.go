// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package awsbedrock

const (
	// StopReasonEndTurn is a StopReason enum value.
	StopReasonEndTurn = "end_turn"

	// StopReasonToolUse is a StopReason enum value.
	StopReasonToolUse = "tool_use"

	// StopReasonMaxTokens is a StopReason enum value.
	StopReasonMaxTokens = "max_tokens"

	// StopReasonStopSequence is a StopReason enum value.
	StopReasonStopSequence = "stop_sequence"

	// StopReasonGuardrailIntervened is a StopReason enum value.
	StopReasonGuardrailIntervened = "guardrail_intervened"

	// StopReasonContentFiltered is a StopReason enum value.
	StopReasonContentFiltered = "content_filtered"

	// ConversationRoleUser is a ConversationRole enum value.
	ConversationRoleUser = "user"

	// ConversationRoleAssistant is a ConversationRole enum value.
	ConversationRoleAssistant = "assistant"
)

// InferenceConfiguration Base inference parameters to pass to a model in a call to Converse (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_Converse.html)
// or ConverseStream (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ConverseStream.html).
// For more information, see Inference parameters for foundation models (https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters.html).
//
// If you need to pass additional parameters that the model supports, use the
// additionalModelRequestFields request field in the call to Converse or ConverseStream.
// For more information, see Model parameters (https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters.html).
type InferenceConfiguration struct {
	// The maximum number of tokens to allow in the generated response. The default
	// value is the maximum allowed value for the model that you are using. For
	// more information, see Inference parameters for foundation models (https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters.html).
	MaxTokens *int64 `json:"maxTokens,omitempty"`

	// A list of stop sequences. A stop sequence is a sequence of characters that
	// causes the model to stop generating the response.
	StopSequences []string `json:"stopSequences,omitempty"`

	// The likelihood of the model selecting higher-probability options while generating
	// a response. A lower value makes the model more likely to choose higher-probability
	// options, while a higher value makes the model more likely to choose lower-probability
	// options.
	//
	// The default value is the default value for the model that you are using.
	// For more information, see Inference parameters for foundation models (https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters.html).
	Temperature *float64 `json:"temperature,omitempty"`

	// The percentage of most-likely candidates that the model considers for the
	// next token. For example, if you choose a value of 0.8 for topP, the model
	// selects from the top 80% of the probability distribution of tokens that could
	// be next in the sequence.
	//
	// The default value is the default value for the model that you are using.
	// For more information, see Inference parameters for foundation models (https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters.html).
	TopP *float64 `json:"topP,omitempty"`
}

// GuardrailConverseTextBlock A text block that contains text that you want to assess with a guardrail.
// For more information, see GuardrailConverseContentBlock.
type GuardrailConverseTextBlock struct {
	// The qualifier details for the guardrails contextual grounding filter.
	Qualifiers []*string `json:"qualifiers,omitempty"`

	// The text that you want to guard.
	//
	// Text is a required field.
	Text *string `json:"text"`
}

// GuardrailConverseContentBlock A content block for selective guarding with the Converse (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_Converse.html)
// or ConverseStream (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ConverseStream.html)
// API operations.
type GuardrailConverseContentBlock struct {
	// The text to guard.
	Text *GuardrailConverseTextBlock `json:"text"`
}

// SystemContentBlock A system content block.
type SystemContentBlock struct {
	// A content block to assess with the guardrail. Use with the Converse (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_Converse.html)
	// or ConverseStream (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ConverseStream.html)
	// API operations.
	//
	// For more information, see Use a guardrail with the Converse API in the Amazon
	// Bedrock User Guide.
	GuardContent *GuardrailConverseContentBlock `json:"guardContent,omitempty"`

	// A system prompt for the model.
	Text *string `json:"text,omitempty"`

	// Cache point for prompt caching. Enables caching of preceding content.
	// See https://docs.aws.amazon.com/bedrock/latest/userguide/prompt-caching.html for more information.
	CachePoint *CachePointBlock `json:"cachePoint,omitempty"`
}

// GuardrailConfiguration Configuration information for a guardrail that you use with the Converse
// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_Converse.html)
// operation.
type GuardrailConfiguration struct {
	// The identifier for the guardrail.
	//
	// GuardrailIdentifier is a required field.
	GuardrailIdentifier *string `json:"guardrailIdentifier"`

	// The version of the guardrail.
	//
	// GuardrailVersion is a required field.
	GuardrailVersion *string `json:"guardrailVersion"`

	// The trace behavior for the guardrail.
	Trace *string `json:"trace,omitempty"`
}

type ConverseInput struct {
	// Additional model parameters field paths to return in the response. Converse
	// returns the requested fields as a JSON Pointer object in the additionalModelResponseFields
	// field. The following is example JSON for additionalModelResponseFieldPaths.
	//
	// [ "/stop_sequence" ]
	//
	// For information about the JSON Pointer syntax, see the Internet Engineering
	// Task Force (IETF) (https://datatracker.ietf.org/doc/html/rfc6901) documentation.
	//
	// Converse rejects an empty JSON Pointer or incorrectly structured JSON Pointer
	// with a 400 error code. if the JSON Pointer is valid, but the requested field
	// is not in the model response, it is ignored by Converse.
	AdditionalModelResponseFieldPaths []*string `json:"additionalModelResponseFieldPaths,omitempty"`

	// Additional inference parameters that the model supports, beyond the base set of inference parameters
	// that Converse and ConverseStream support in the inferenceConfig field.
	// For more information, see https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_Converse.html#bedrock-runtime_Converse-request-additionalModelRequestFields.
	AdditionalModelRequestFields map[string]interface{} `json:"additionalModelRequestFields,omitempty"`

	// Configuration information for a guardrail that you want to use in the request.
	GuardrailConfig *GuardrailConfiguration `json:"guardrailConfig,omitempty"`

	// Inference parameters to pass to the model. Converse supports a base set of
	// inference parameters. If you need to pass additional parameters that the
	// model supports, use the additionalModelRequestFields request field.
	InferenceConfig *InferenceConfiguration `json:"inferenceConfig,omitempty"`

	// The messages that you want to send to the model.
	//
	// Messages is a required field.
	Messages []*Message `json:"messages"`

	// A system prompt to pass to the model.
	System []*SystemContentBlock `json:"system,omitempty"`

	// Tier to optimize for availability, cost, and performance.
	//
	// Can be "reserved", "priority", "flex", and "default".
	// For more information, see https://docs.aws.amazon.com/bedrock/latest/userguide/service-tiers-inference.html.
	ServiceTier *ServiceTier `json:"serviceTier,omitempty"`

	// Configuration information for the tools that the model can use when generating
	// a response.
	//
	// This field is only supported by Anthropic Claude 3, Cohere Command R, Cohere
	// Command R+, and Mistral Large models.
	ToolConfig *ToolConfiguration `json:"toolConfig,omitempty"`
}

// Message A message input, or returned from, a call to Converse (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_Converse.html)
// or ConverseStream (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ConverseStream.html).
type Message struct {
	// The message content. Note the following restrictions:
	//
	//    * You can include up to 20 images. Each image's size, height, and width
	//    must be no more than 3.75 MB, 8000 px, and 8000 px, respectively.
	//
	//    * You can include up to five documents. Each document's size must be no
	//    more than 4.5 MB.
	//
	//    * If you include a ContentBlock with a document field in the array, you
	//    must also include a ContentBlock with a text field.
	//
	//    * You can only include images and documents if the role is user.
	//
	// Content is a required field.
	Content []*ContentBlock `json:"content"`

	// The role that the message plays in the message.
	//
	// Role is a required field.
	Role string `json:"role"`
}

// ImageSource The source for an image.
type ImageSource struct {
	// The raw image bytes for the image. If you use an AWS SDK, you don't need
	// to encode the image bytes in base64.
	// Bytes are automatically base64 encoded/decoded by the SDK.
	Bytes []byte `json:"bytes"`
}

// ImageBlock Image content for a message.
type ImageBlock struct {
	// The format of the image.
	//
	// Format is a required field.
	Format string `json:"format"`

	// The source for the image.
	//
	// Source is a required field.
	Source ImageSource `json:"source"`
}

// DocumentSource Contains the content of a document.
type DocumentSource struct {
	// The raw bytes for the document. If you use an Amazon Web Services SDK, you
	// don't need to encode the bytes in base64.
	// Bytes are automatically base64 encoded/decoded by the SDK.
	Bytes []byte `json:"bytes"`
}

// DocumentBlock A document to include in a message.
type DocumentBlock struct {
	// The format of a document, or its extension.
	//
	// Format is a required field.
	Format string `json:"format"`

	// A name for the document. The name can only contain the following characters:
	//
	//    * Alphanumeric characters
	//
	//    * Whitespace characters (no more than one in a row)
	//
	//    * Hyphens
	//
	//    * Parentheses
	//
	//    * Square brackets
	//
	// This field is vulnerable to prompt injections, because the model might inadvertently
	// interpret it as instructions. Therefore, we recommend that you specify a
	// neutral name.
	//
	// Name is a required field.
	Name string `json:"name"`

	// Contains the content of the document.
	//
	// Source is a required field.
	Source DocumentSource `json:"source"`
}

// ReasoningTextBlock contains the reasoning text and an optional signature.
// See: https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ReasoningTextBlock.html
type ReasoningTextBlock struct {
	// The reasoning that the model used to return the output.
	Text string `json:"text"`
	// A token that verifies that the reasoning text was generated by the model.
	Signature string `json:"signature,omitzero"`
}

// ReasoningContentBlock contains the reasoning trace for the inference that the model ran.
// See: https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ReasoningContentBlock.html
type ReasoningContentBlock struct {
	// The reasoning that the model used to return the output.
	ReasoningText *ReasoningTextBlock `json:"reasoningText,omitempty"`
	// The content that has been redacted from the reasoning trace.
	RedactedContent []byte `json:"redactedContent,omitempty"`
}

// ReasoningContentBlockDelta contains the reasoning trace for the inference that the model ran.
// See: https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ReasoningContentBlockDelta.html
type ReasoningContentBlockDelta struct {
	// The reasoning that the model used to return the output.
	Text string `json:"text,omitzero"`
	// A token that verifies that the reasoning text was generated by the model.
	Signature string `json:"signature,omitzero"`
	// The content that has been redacted from the reasoning trace.
	RedactedContent []byte `json:"redactedContent,omitempty"`
}

// ToolResultContentBlock The tool result content block.
type ToolResultContentBlock struct {
	// A tool result that is a document.
	Document *DocumentBlock `json:"document,omitempty"`

	// A tool result that is an image.
	//
	// This field is only supported by Anthropic Claude 3 models.
	Image *ImageBlock `json:"image,omitempty"`

	// A tool result that is text.
	Text *string `json:"text,omitempty"`

	// A tool result that is JSON format data.
	JSON *string `json:"json,omitempty"`
}

// ToolResultBlock A tool result block that contains the results for a tool request that the
// model previously made.
type ToolResultBlock struct {
	// The content for tool result content block.
	//
	// Content is a required field.
	Content []*ToolResultContentBlock `json:"content"`

	// The status for the tool result content block.
	//
	// This field is only supported Anthropic Claude 3 models.
	Status *string `json:"status"`

	// The ID of the tool request that this is the result for.
	//
	// ToolUseId is a required field.
	ToolUseID *string `json:"toolUseId"`
}

// ToolUseBlock A tool use block contains information about a tool that the model is requesting be run.
// The model uses the result from the tool to generate a response.
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ToolUseBlock.html
type ToolUseBlock struct {
	// Name is the name the tool that the model wants to use.
	Name string `json:"name"`
	// Input is to pass to the tool in JSON format.
	Input map[string]any `json:"input"`
	// ToolUseID is the ID for the tool request, pattern is ^[a-zA-Z0-9_-]+$.
	ToolUseID string `json:"toolUseId"`
}

// ContentBlock is defined in the AWS Bedrock API:
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ContentBlock.html
type ContentBlock struct {
	// A tool result that is a document.
	Document *DocumentBlock `json:"document,omitempty"`

	// A tool result that is an image.
	//
	// This field is only supported by Anthropic Claude 3 models.
	Image *ImageBlock `json:"image,omitempty"`
	// Text to include in the message.
	Text *string `json:"text,omitempty"`

	// The result for a tool request that a model makes.
	ToolResult *ToolResultBlock `json:"toolResult,omitempty"`

	// Information about a tool use request from a model.
	ToolUse *ToolUseBlock `json:"toolUse,omitempty"`

	// Contains content regarding the reasoning that is carried out by the model. Reasoning refers to a Chain of Thought (CoT) that the model generates to enhance the accuracy of its final response.
	// Note: This object is a Union. Only one member of this object can be specified or returned.
	// Required: No
	// See https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ReasoningContentBlock.html for more information.
	ReasoningContent *ReasoningContentBlock `json:"reasoningContent,omitempty"`

	// Cache point for prompt caching. Enables caching of preceding content.
	// See https://docs.aws.amazon.com/bedrock/latest/userguide/prompt-caching.html for more information.
	CachePoint *CachePointBlock `json:"cachePoint,omitempty"`
}

// ConverseMetrics Metrics for a call to Converse (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_Converse.html).
type ConverseMetrics struct {
	// The latency of the call to Converse, in milliseconds.
	//
	// LatencyMs is a required field.
	LatencyMs *int64 `json:"latencyMs"`
}

// ConverseResponse is the response from a call to Converse.
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_Converse.html
type ConverseResponse struct {
	// Metrics for the call to Converse.
	//
	// Metrics is a required field.
	Metrics *ConverseMetrics `json:"metrics"`

	// The result from the call to Converse.
	//
	// Output is a required field.
	Output *ConverseOutput `json:"output"`

	// The reason why the model stopped generating output.
	//
	// StopReason is a required field.
	//
	// Valid Values: end_turn | tool_use | max_tokens | stop_sequence | guardrail_intervened | content_filtered.
	StopReason *string `json:"stopReason"`

	// Specifies the processing tier configuration used for serving the request.
	//
	// For more information, see https://docs.aws.amazon.com/bedrock/latest/userguide/service-tiers-inference.html.
	ServiceTier *ServiceTier `json:"serviceTier,omitempty"`

	// The total number of tokens used in the call to Converse. The total includes
	// the tokens input to the model and the tokens generated by the model.
	//
	// Usage is a required field.
	Usage *TokenUsage `json:"usage"`
}

// ConverseOutput is defined in the AWS Bedrock API:
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ConverseOutput.html
type ConverseOutput struct {
	Message Message `json:"message,omitempty"`
}

// ServiceTier is defined in the AWS Bedrock API:
// https://docs.aws.amazon.com/bedrock/latest/userguide/service-tiers-inference.html
type ServiceTier struct {
	Type string `json:"type"`
}

// TokenUsage is defined in the AWS Bedrock API:
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_TokenUsage.html
type TokenUsage struct {
	InputTokens           int64  `json:"inputTokens"`
	OutputTokens          int64  `json:"outputTokens"`
	TotalTokens           int64  `json:"totalTokens"`
	CacheReadInputTokens  *int64 `json:"cacheReadInputTokens,omitempty"`
	CacheWriteInputTokens *int64 `json:"cacheWriteInputTokens,omitempty"`
}

// ConverseStreamEventType represents a distinct event type received from the Bedrock ConverseStream API.
// Using a string type provides a clear, human-readable value, which is often helpful
// when logging or debugging stream events.
type ConverseStreamEventType string

const (
	// ConverseStreamEventTypeUnknown is the zero value and represents an uninitialized or unknown event type.
	// This is a Go best practice to ensure the zero value is not a valid constant.
	ConverseStreamEventTypeUnknown ConverseStreamEventType = ""

	// ConverseStreamEventTypeMessageStart signals the start of the assistant's message.
	ConverseStreamEventTypeMessageStart ConverseStreamEventType = "messageStart"

	// ConverseStreamEventTypeContentBlockStart signals the start of a content block within the message (e.g., text or tool use).
	ConverseStreamEventTypeContentBlockStart ConverseStreamEventType = "contentBlockStart"

	// ConverseStreamEventTypeContentBlockDelta contains a chunk of content (e.g., text or tool input).
	ConverseStreamEventTypeContentBlockDelta ConverseStreamEventType = "contentBlockDelta"

	// ConverseStreamEventTypeContentBlockStop signals the end of a content block.
	ConverseStreamEventTypeContentBlockStop ConverseStreamEventType = "contentBlockStop"

	// ConverseStreamEventTypeMessageStop signals the end of the entire message.
	ConverseStreamEventTypeMessageStop ConverseStreamEventType = "messageStop"

	// ConverseStreamEventTypeMetadata contains usage and latency information.
	ConverseStreamEventTypeMetadata ConverseStreamEventType = "metadata"
)

// String implements the fmt.Stringer interface for better logging/printing.
func (c ConverseStreamEventType) String() string {
	return string(c)
}

// ConverseStreamEvent is the union of all possible event types in the AWS Bedrock API:
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ConverseStream.html
type ConverseStreamEvent struct {
	EventType         string                                `json:"eventType"`
	ContentBlockIndex int                                   `json:"contentBlockIndex,omitempty"`
	Delta             *ConverseStreamEventContentBlockDelta `json:"delta,omitempty"`
	Role              *string                               `json:"role,omitempty"`
	StopReason        *string                               `json:"stopReason,omitempty"`
	Usage             *TokenUsage                           `json:"usage,omitempty"`
	Start             *ContentBlockStart                    `json:"start,omitempty"`
	ServiceTier       *ServiceTier                          `json:"serviceTier,omitempty"`
}

// ConverseStreamEventContentBlockDelta is defined in the AWS Bedrock API:
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ContentBlockDelta.html
type ConverseStreamEventContentBlockDelta struct {
	Text             *string                     `json:"text,omitempty"`
	ToolUse          *ToolUseBlockDelta          `json:"toolUse,omitempty"`
	ReasoningContent *ReasoningContentBlockDelta `json:"reasoningContent,omitempty"`
}

// ContentBlockStart is the start information.
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ContentBlockStart.html
type ContentBlockStart struct {
	ToolUse *ToolUseBlockStart `json:"toolUse,omitempty"`
}

// ToolUseBlockStart is the start of a tool use block.
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ToolUseBlockStart.html
type ToolUseBlockStart struct {
	Name      string `json:"name"`
	ToolUseID string `json:"toolUseId"`
}

// ToolUseBlockDelta is the delta for a tool use block.
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ToolUseBlockDelta.html
type ToolUseBlockDelta struct {
	Input string `json:"input"`
}

type BedrockException struct {
	// Status code of the aws error response.
	Code string `json:"code,omitempty"`
	// Error type of the aws response.
	Type string `json:"type,omitempty"`
	// Error message of the aws response.
	Message string `json:"message"`
}

// AnyToolChoice The model must request at least one tool (no text is generated). For example,
// {"any" : {}}.
type AnyToolChoice struct{}

// AutoToolChoice The Model automatically decides if a tool should be called or whether to
// generate text instead. For example, {"auto" : {}}.
type AutoToolChoice struct{}

// SpecificToolChoice The model must request a specific tool. For example, {"tool" : {"name" :
// "Your tool name"}}.
//
// This field is only supported by Anthropic Claude 3 models.
type SpecificToolChoice struct {
	// The name of the tool that the model must request.
	//
	// Name is a required field.
	Name *string `json:"name"`
}

// ToolChoice Determines which tools the model should request in a call to Converse or
// ConverseStream. ToolChoice is only supported by Anthropic Claude 3 models
// and by Mistral AI Mistral Large.
type ToolChoice struct {
	// The model must request at least one tool (no text is generated).
	Any *AnyToolChoice `json:"any,omitempty"`

	// (Default). The Model automatically decides if a tool should be called or
	// whether to generate text instead.
	Auto *AutoToolChoice `json:"auto,omitempty"`

	// The Model must request the specified tool. Only supported by Anthropic Claude
	// 3 models.
	Tool *SpecificToolChoice `json:"tool,omitempty"`
}

// ToolConfiguration Configuration information for the tools that you pass to a model. For more
// information, see Tool use (function calling) (https://docs.aws.amazon.com/bedrock/latest/userguide/tool-use.html)
// in the Amazon Bedrock User Guide.
//
// This field is only supported by Anthropic Claude 3, Cohere Command R, Cohere
// Command R+, and Mistral Large models.
type ToolConfiguration struct {
	// If supported by model, forces the model to request a tool.
	ToolChoice *ToolChoice `json:"toolChoice,omitempty"`

	// An array of tools that you want to pass to a model.
	//
	// Tools is a required field.
	Tools []*Tool `json:"tools"`
}

// Tool Information about a tool that you can use with the Converse API. For more
// information, see Tool use (function calling) (https://docs.aws.amazon.com/bedrock/latest/userguide/tool-use.html)
// in the Amazon Bedrock User Guide.
type Tool struct {
	// The specification for the tool. Omitted when this element only carries a cache point,
	// as Bedrock rejects an element that sets more than one of the union's members.
	ToolSpec *ToolSpecification `json:"toolSpec,omitempty"`

	// Cache point for prompt caching. Enables caching of preceding content.
	// See https://docs.aws.amazon.com/bedrock/latest/userguide/prompt-caching.html for more information.
	CachePoint *CachePointBlock `json:"cachePoint,omitempty"`
}

// ToolInputSchema The schema for the tool. The top level schema type must be an object.
type ToolInputSchema struct {
	JSON any `json:"json"`
}

// ToolSpecification The specification for the tool.
type ToolSpecification struct {
	// The description for the tool.
	Description *string `json:"description,omitzero"`

	// The schema for the tool in JSON format.
	//
	// InputSchema is a required field.
	InputSchema *ToolInputSchema `json:"inputSchema"`

	// The name for the tool.
	//
	// Name is a required field.
	Name *string `json:"name"`
}

// CachePointBlock defines a cache checkpoint for prompt caching.
// This allows AWS Bedrock models to cache content up to this point for reuse in subsequent requests.
// See https://docs.aws.amazon.com/bedrock/latest/userguide/prompt-caching.html for more information.
type CachePointBlock struct {
	// The type of cache point. Currently only "default" is supported.
	Type string `json:"type"`
}

// TitanEmbeddingRequest is the request body for the Amazon Titan Embed Text models
// via the AWS Bedrock InvokeModel API.
//
// v1 (amazon.titan-embed-text-v1): only InputText is supported.
// v2 (amazon.titan-embed-text-v2:0): Dimensions, Normalize, and EmbeddingTypes are also supported.
//
// See https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-titan-embed-text.html
type TitanEmbeddingRequest struct {
	// InputText is the text to embed. Required.
	InputText string `json:"inputText"`

	// Dimensions is the number of dimensions for the output embedding.
	// Accepted values: 256, 512, 1024 (default). Only supported by v2.
	Dimensions *int `json:"dimensions,omitempty"`

	// Normalize indicates whether to normalize the output embedding vector.
	// Defaults to true. Only supported by v2.
	Normalize *bool `json:"normalize,omitempty"`

	// EmbeddingTypes specifies the output embedding types.
	// Accepted values: "float" (default), "binary". Only supported by v2.
	EmbeddingTypes []string `json:"embeddingTypes,omitempty"`
}

// TitanEmbeddingResponse is the response body returned by the Amazon Titan Embed Text models.
//
// See https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-titan-embed-text.html
type TitanEmbeddingResponse struct {
	// Embedding is the embedding vector for the input text.
	Embedding []float64 `json:"embedding"`

	// InputTextTokenCount is the number of tokens in the input text.
	InputTextTokenCount int `json:"inputTextTokenCount"`
}

// CountTokensConverseInput mirrors the Converse input for the Bedrock CountTokens API.
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_CountTokens.html
type CountTokensConverseInput struct {
	Messages   []*Message            `json:"messages,omitempty"`
	System     []*SystemContentBlock `json:"system,omitempty"`
	ToolConfig *ToolConfiguration    `json:"toolConfig,omitempty"`
}

// CountTokensConverseRequest is the request structure for the Bedrock CountTokens API using Converse-style input.
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_CountTokens.html
type CountTokensConverseRequest struct {
	Input struct {
		Converse *CountTokensConverseInput `json:"converse"`
	} `json:"input"`
}

// CountTokensInvokeModelRequest is the request structure for the Bedrock CountTokens API using InvokeModel-style input.
// The body is a base64-encoded model-specific request body (e.g., Anthropic Messages format).
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_CountTokens.html
type CountTokensInvokeModelRequest struct {
	Input struct {
		InvokeModel struct {
			Body string `json:"body"`
		} `json:"invokeModel"`
	} `json:"input"`
}

// CountTokensResponse represents the response structure for the AWS Bedrock CountTokens API
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_CountTokens.html
type CountTokensResponse struct {
	// The total number of input tokens that were counted
	InputTokens int `json:"inputTokens"`
}
