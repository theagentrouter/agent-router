// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"github.com/envoyproxy/ai-gateway/internal/endpointspec"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

// policyTransportFor selects where an inference policy merges its platform
// prompt for an endpoint, given the backend schema the request body was
// translated to. Operations not listed keep field-based detection.
func policyTransportFor[EndpointSpecT any](schema filterapi.APISchemaName) inferencePolicyTransport {
	var spec EndpointSpecT
	switch any(spec).(type) {
	case endpointspec.ResponsesEndpointSpec, endpointspec.ResponsesCompactEndpointSpec:
		if schema == filterapi.APISchemaOpenAI || schema == filterapi.APISchemaAzureOpenAI {
			return policyTransportResponses
		}
	case endpointspec.MessagesEndpointSpec:
		switch schema {
		case filterapi.APISchemaAnthropic, filterapi.APISchemaGCPAnthropic, filterapi.APISchemaAWSAnthropic:
			return policyTransportAnthropic
		case filterapi.APISchemaOpenAI:
			// The body was translated to an OpenAI chat completion.
			return policyTransportChat
		}
	case endpointspec.ChatCompletionsBatchEndpointSpec:
		return policyTransportChatBatch
	case endpointspec.PassthroughEndpointSpec:
		return policyTransportNone
	}
	return policyTransportByField
}
