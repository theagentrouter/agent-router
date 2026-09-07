// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package v1beta1

import (
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// VersionedAPISchema defines the API schema of either AIGatewayRoute (the input) or AIServiceBackend (the output).
//
// This allows the ai-gateway to understand the input and perform the necessary transformation
// depending on the API schema pair (input, output).
//
// Note that this is vendor specific, and the stability of the API schema is not guaranteed by
// the ai-gateway, but by the vendor via proper versioning.
type VersionedAPISchema struct {
	// Name is the name of the API schema of the AIGatewayRoute or AIServiceBackend.
	//
	// +kubebuilder:validation:Enum=OpenAI;Cohere;AWSBedrock;AzureOpenAI;GCPVertexAI;GCPAnthropic;Anthropic;AWSAnthropic;AWSOpenAI
	Name APISchema `json:"name"`

	// Version is the version of the API schema.
	//
	// When the name is set to AzureOpenAI, this version maps to "API Version" in the
	// Azure OpenAI API documentation (https://learn.microsoft.com/en-us/azure/ai-services/openai/reference#rest-api-versioning).
	// This field is ignored for OpenAI, AWSBedrock, GCPVertexAI, and Anthropic.
	// For OpenAI and Anthropic, use prefix to configure custom request paths.
	//
	// See https://aigateway.envoyproxy.io/docs/capabilities/llm-integrations/supported-providers for details.
	// +optional
	Version *string `json:"version,omitempty"`

	// Prefix is the prefix for the API.
	//
	// When the name is set to "OpenAI", "chat completions" API endpoint will be "${this_field}/chat/completions".
	// When the name is set to "Anthropic", the "messages" API endpoint will be "${this_field}/messages".
	// It can be with or without a leading slash ("/").
	// This field is ignored for AWSAnthropic and GCPAnthropic.
	//
	// This is especially useful when routing to a backend that has an OpenAI or Anthropic compatible API but has a different
	// prefix. For example, Gemini OpenAI compatible API (https://ai.google.dev/gemini-api/docs/openai) uses
	// "/v1beta/openai" prefix. Another example is that Cohere AI (https://docs.cohere.com/v2/docs/compatibility-api)
	// uses "/compatibility/v1" prefix. On the other hand, DeepSeek (https://api-docs.deepseek.com/) doesn't
	// use prefix, so you can leave this field unset.
	//
	// See https://aigateway.envoyproxy.io/docs/capabilities/llm-integrations/supported-providers for details.
	// +optional
	Prefix *string `json:"prefix,omitempty"`
}

// APISchema defines the API schema.
type APISchema string

const (
	// APISchemaOpenAI is the OpenAI schema.
	//
	// https://github.com/openai/openai-openapi
	APISchemaOpenAI APISchema = "OpenAI"
	// APISchemaCohere is the Cohere schema.
	//
	// https://docs.cohere.com/v2
	APISchemaCohere APISchema = "Cohere"
	// APISchemaAWSBedrock is the AWS Bedrock schema.
	//
	// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_Operations_Amazon_Bedrock_Runtime.html
	APISchemaAWSBedrock APISchema = "AWSBedrock"
	// APISchemaAzureOpenAI APISchemaAzure is the Azure OpenAI schema.
	//
	// https://learn.microsoft.com/en-us/azure/ai-services/openai/reference#api-specs
	APISchemaAzureOpenAI APISchema = "AzureOpenAI"
	// APISchemaGCPVertexAI is the schema followed by Gemini models hosted on GCP's Vertex AI platform.
	// Note: Using this schema requires a BackendSecurityPolicy to be configured and attached,
	// as the transformation will use the gcp-region and project-name from the BackendSecurityPolicy.
	//
	// https://cloud.google.com/vertex-ai/docs/reference/rest/v1/projects.locations.endpoints/generateContent?hl=en
	APISchemaGCPVertexAI APISchema = "GCPVertexAI"
	// APISchemaGCPAnthropic is the schema for Anthropic models hosted on GCP's Vertex AI platform.
	// Returns native Anthropic format responses for seamless integration.
	//
	// https://docs.anthropic.com/en/api/claude-on-vertex-ai
	APISchemaGCPAnthropic APISchema = "GCPAnthropic"
	// APISchemaAnthropic is the native Anthropic API schema.
	// https://docs.claude.com/en/home
	APISchemaAnthropic APISchema = "Anthropic"
	// APISchemaAWSAnthropic is the schema for Anthropic models hosted on AWS Bedrock.
	// Uses the native Anthropic Messages API format for requests and responses.
	// When used with /v1/chat/completions endpoint, translates OpenAI format to Anthropic.
	// When used with /v1/messages endpoint, passes through native Anthropic format.
	//
	// https://aws.amazon.com/bedrock/anthropic/
	// https://docs.claude.com/en/api/claude-on-amazon-bedrock
	APISchemaAWSAnthropic APISchema = "AWSAnthropic"
	// APISchemaAWSOpenAI is the OpenAI-compatible API schema provided by AWS.
	APISchemaAWSOpenAI APISchema = "AWSOpenAI"
)

const (
	// AIModelHeaderKey is the header key whose value is extracted from the request by the ai-gateway.
	// This can be used to describe the routing behavior in HTTPRoute referenced by AIGatewayRoute.
	AIModelHeaderKey = "x-ai-eg-model"
)

// LLMRequestCost configures each request cost.
type LLMRequestCost struct {
	// MetadataKey is the key of the metadata to store this cost of the request.
	//
	// +kubebuilder:validation:Required
	MetadataKey string `json:"metadataKey"`
	// Type specifies the type of the request cost. The default is "OutputToken",
	// and it uses "output token" as the cost. The other types are "InputToken", "TotalToken",
	// "CachedInputToken", "CacheCreationInputToken", "ReasoningToken", and "CEL".
	//
	// +kubebuilder:validation:Enum=OutputToken;InputToken;CachedInputToken;CacheCreationInputToken;TotalToken;ReasoningToken;CEL
	Type LLMRequestCostType `json:"type"`
	// CEL is the CEL expression to calculate the cost of the request.
	// The CEL expression must return a signed or unsigned integer. If the
	// return value is negative, it will be error.
	//
	// The expression can use the following variables:
	//
	//	* model: the model name extracted from the request content. Type: string.
	//	* backend: the backend name in the form of "name.namespace". Type: string.
	//	* input_tokens: the number of input tokens. Type: unsigned integer.
	//	* cached_input_tokens: the number of cached read input tokens. Type: unsigned integer.
	//	* cache_creation_input_tokens: the number of cache creation input tokens. Type: unsigned integer.
	//	* output_tokens: the number of output tokens. Type: unsigned integer.
	//	* total_tokens: the total number of tokens. Type: unsigned integer.
	//	* reasoning_tokens: the number of reasoning tokens. Type: unsigned integer.
	//
	// For example, the following expressions are valid:
	//
	// 	* "model == 'llama' ?  input_tokens + output_token * 0.5 : total_tokens"
	//	* "backend == 'foo.default' ?  input_tokens + output_tokens : total_tokens"
	//	* "backend == 'bar.default' ?  (input_tokens - cached_input_tokens) + cached_input_tokens * 0.1 + cache_creation_input_tokens * 1.25 + output_tokens : total_tokens"
	//	* "input_tokens + output_tokens + total_tokens"
	//	* "input_tokens * output_tokens"
	//
	// +optional
	CEL *string `json:"cel,omitempty"`
}

// LLMRequestCostType specifies the type of the LLMRequestCost.
type LLMRequestCostType string

const (
	// LLMRequestCostTypeInputToken is the cost type of the input token.
	LLMRequestCostTypeInputToken LLMRequestCostType = "InputToken"
	// LLMRequestCostTypeCachedInputToken is the cost type of the cached input token.
	LLMRequestCostTypeCachedInputToken LLMRequestCostType = "CachedInputToken"
	// LLMRequestCostTypeCacheCreationInputToken is the cost type of the cached input token.
	LLMRequestCostTypeCacheCreationInputToken LLMRequestCostType = "CacheCreationInputToken"
	// LLMRequestCostTypeOutputToken is the cost type of the output token.
	LLMRequestCostTypeOutputToken LLMRequestCostType = "OutputToken"
	// LLMRequestCostTypeTotalToken is the cost type of the total token.
	LLMRequestCostTypeTotalToken LLMRequestCostType = "TotalToken"
	// LLMRequestCostTypeReasoningToken is the cost type of the reasoning token.
	LLMRequestCostTypeReasoningToken LLMRequestCostType = "ReasoningToken"
	// LLMRequestCostTypeCEL is for calculating the cost using the CEL expression.
	LLMRequestCostTypeCEL LLMRequestCostType = "CEL"
)

const (
	// AIGatewayFilterMetadataNamespace is the namespace for the ai-gateway filter metadata.
	AIGatewayFilterMetadataNamespace = "io.envoy.ai_gateway"
)

// HTTPHeaderValueFilter filters individual values out of a multi-valued request header before the
// request is forwarded to this backend.
//
// This drops (or restricts to) specific values from a comma-separated header while forwarding the
// rest untouched, so one unsupported value does not fail the whole request.
//
// Only comma-delimited headers are supported. Values are matched exactly, after surrounding
// whitespace is trimmed. To remove a header entirely, use HeaderMutation.Remove instead.
type HTTPHeaderValueFilter struct {
	// Name is the name of the header whose values are filtered. Header names are
	// case-insensitive (see https://datatracker.ietf.org/doc/html/rfc2616#section-4.2).
	//
	// +kubebuilder:validation:Required
	Name gwapiv1.HTTPHeaderName `json:"name"`

	// Mode selects the filtering semantics. Defaults to Denylist.
	//
	// +optional
	// +kubebuilder:default=Denylist
	Mode HTTPHeaderValueFilterMode `json:"mode,omitempty"`

	// Values is the list of header values to drop (Denylist) or keep (Allowlist).
	// An empty list leaves the header unchanged.
	//
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=64
	Values []string `json:"values,omitempty"`
}

// HTTPHeaderValueFilterMode specifies the filtering semantics of an HTTPHeaderValueFilter.
//
// +kubebuilder:validation:Enum=Denylist;Allowlist
type HTTPHeaderValueFilterMode string

const (
	// HTTPHeaderValueFilterModeDenylist drops the values listed in Values, keeping everything else.
	// New values the gateway has not been configured for pass through untouched.
	HTTPHeaderValueFilterModeDenylist HTTPHeaderValueFilterMode = "Denylist"
	// HTTPHeaderValueFilterModeAllowlist keeps only the values listed in Values, dropping everything
	// else. New values the gateway has not been configured for are dropped.
	HTTPHeaderValueFilterModeAllowlist HTTPHeaderValueFilterMode = "Allowlist"
)

// HTTPHeaderMutation defines the mutation of HTTP headers that will be applied to the request
type HTTPHeaderMutation struct {
	// Set overwrites/adds the request with the given header (name, value)
	// before the action.
	//
	// Input:
	//   GET /foo HTTP/1.1
	//   my-header: foo
	//
	// Config:
	//   set:
	//   - name: "my-header"
	//     value: "bar"
	//
	// Output:
	//   GET /foo HTTP/1.1
	//   my-header: bar
	//
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	Set []gwapiv1.HTTPHeader `json:"set,omitempty"`

	// Remove the given header(s) from the HTTP request before the action. The
	// value of Remove is a list of HTTP header names. Note that the header
	// names are case-insensitive (see
	// https://datatracker.ietf.org/doc/html/rfc2616#section-4.2).
	//
	// Input:
	//   GET /foo HTTP/1.1
	//   my-header1: foo
	//   my-header2: bar
	//   my-header3: baz
	//
	// Config:
	//   remove: ["my-header1", "my-header3"]
	//
	// Output:
	//   GET /foo HTTP/1.1
	//   my-header2: bar
	//
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=16
	Remove []string `json:"remove,omitempty"`
}
