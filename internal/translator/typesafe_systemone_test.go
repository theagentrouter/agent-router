// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"

	typesafeschema "github.com/envoyproxy/ai-gateway/internal/apischema/typesafe"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

const systemOneRequestBody = `{"model":"jev-latest","state":{"subject":"Refund for order #4471"},"questions":{"team":{"type":"choice","instructions":"Which team?","criteria":{"billing":null,"shipping":null}}}}`

func TestTypeSafeToTypeSafeTranslatorSystemOne_RequestBody(t *testing.T) {
	for _, tc := range []struct {
		name              string
		modelNameOverride string
		onRetry           bool
		expBodyContains   string
	}{
		{name: "valid_body"},
		{name: "model_name_override", modelNameOverride: "jev-1.13.0", expBodyContains: `"model":"jev-1.13.0"`},
		{name: "on_retry_no_change", onRetry: true},
		{name: "model_name_override_with_retry", modelNameOverride: "jev-preview", onRetry: true, expBodyContains: `"model":"jev-preview"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			translator := NewSystemOneTypeSafeToTypeSafeTranslator("v1", tc.modelNameOverride)
			var req typesafeschema.SystemOneRequest
			require.NoError(t, json.Unmarshal([]byte(systemOneRequestBody), &req))

			headerMutation, bodyMutation, err := translator.RequestBody([]byte(systemOneRequestBody), &req, tc.onRetry)
			require.NoError(t, err)
			require.NotEmpty(t, headerMutation)
			require.Equal(t, pathHeaderName, headerMutation[0].Key())
			require.Equal(t, "/v1/systemone", headerMutation[0].Value())

			switch {
			case tc.expBodyContains != "":
				require.NotNil(t, bodyMutation)
				require.Contains(t, string(bodyMutation), tc.expBodyContains)
				// The polymorphic fields must be preserved byte-for-byte.
				require.Contains(t, string(bodyMutation), `"state":{"subject":"Refund for order #4471"}`)
				require.Contains(t, string(bodyMutation), `"criteria":{"billing":null,"shipping":null}`)
				require.Len(t, headerMutation, 2)
				require.Equal(t, contentLengthHeaderName, headerMutation[1].Key())
			case bodyMutation != nil:
				require.Len(t, headerMutation, 2)
				require.Equal(t, contentLengthHeaderName, headerMutation[1].Key())
			default:
				require.Len(t, headerMutation, 1)
			}
		})
	}
}

func TestTypeSafeToTypeSafeTranslatorSystemOne_RequestBody_SetModelNameError(t *testing.T) {
	orig := sjsonOptions
	sjsonOptions = &sjson.Options{Optimistic: false, ReplaceInPlace: false}
	t.Cleanup(func() { sjsonOptions = orig })

	translator := NewSystemOneTypeSafeToTypeSafeTranslator("v1", "override-model")
	var req typesafeschema.SystemOneRequest
	headerMutation, bodyMutation, err := translator.RequestBody([]byte("[]"), &req, false)
	require.ErrorContains(t, err, "failed to set model name")
	require.Nil(t, headerMutation)
	require.Nil(t, bodyMutation)
}

func TestTypeSafeToTypeSafeTranslatorSystemOne_ResponseHeaders(t *testing.T) {
	translator := NewSystemOneTypeSafeToTypeSafeTranslator("v1", "")
	headerMutation, err := translator.ResponseHeaders(map[string]string{})
	require.NoError(t, err)
	require.Nil(t, headerMutation)
}

func TestTypeSafeToTypeSafeTranslatorSystemOne_ResponseBody(t *testing.T) {
	for _, tc := range []struct {
		name             string
		responseBody     string
		expectedInput    int32
		expectedOutput   int32
		expectedTotal    int32
		expResponseModel string
		expError         bool
	}{
		{
			name: "all_answer_types",
			responseBody: `{
"model": "jev-1.13.0",
"answers": {
  "is_billing": {"type": "noul", "noul": 0.93},
  "team": {"type": "choice", "choice": "billing", "probabilities": {"billing": 0.91, "shipping": 0.09}, "confidence": 0.88},
  "urgency": {"type": "score", "score": 2.7, "probabilities": {"1": 0.05, "2": 0.25, "3": 0.7},
              "legend": {"1": "can wait", "2": {"summary": "this week"}, "3": ["today", "outage"]}, "confidence": 0.71}
},
"usage": {"input_tokens": 312, "output_tokens": 48}
}`,
			expectedInput:    312,
			expectedOutput:   48,
			expectedTotal:    360,
			expResponseModel: "jev-1.13.0",
		},
		{
			name:             "input_only_usage",
			responseBody:     `{"model": "jev-1.13.0", "answers": {}, "usage": {"input_tokens": 25}}`,
			expectedInput:    25,
			expectedOutput:   -1,
			expectedTotal:    25,
			expResponseModel: "jev-1.13.0",
		},
		{
			name:             "no_usage_falls_back_to_request_model",
			responseBody:     `{"answers": {}}`,
			expectedInput:    -1,
			expectedOutput:   -1,
			expectedTotal:    -1,
			expResponseModel: "jev-latest",
		},
		{
			name:         "invalid_json",
			responseBody: `invalid json`,
			expError:     true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			translator := NewSystemOneTypeSafeToTypeSafeTranslator("v1", "")
			translator.(*typeSafeToTypeSafeTranslatorSystemOne).requestModel = "jev-latest"
			headerMutation, bodyMutation, tokenUsage, responseModel, err := translator.ResponseBody(nil, strings.NewReader(tc.responseBody), true, nil)
			if tc.expError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			expected := tokenUsageFrom(tc.expectedInput, -1, -1, tc.expectedOutput, tc.expectedTotal, -1)
			require.Equal(t, expected, tokenUsage)
			require.Equal(t, tc.expResponseModel, responseModel)
			require.Nil(t, headerMutation)
			require.Nil(t, bodyMutation)
		})
	}
}

type mockSystemOneSpan struct {
	recorded *typesafeschema.SystemOneResponse
}

func (m *mockSystemOneSpan) EndSpan()                   {}
func (m *mockSystemOneSpan) EndSpanOnError(int, []byte) {}
func (m *mockSystemOneSpan) RecordResponse(resp *typesafeschema.SystemOneResponse) {
	m.recorded = resp
}
func (m *mockSystemOneSpan) RecordResponseChunk(*struct{}) {}

func TestTypeSafeToTypeSafeTranslatorSystemOne_ResponseBody_RecordsResponseInSpan(t *testing.T) {
	span := &mockSystemOneSpan{}
	translator := NewSystemOneTypeSafeToTypeSafeTranslator("v1", "")
	_, _, _, _, err := translator.ResponseBody(nil, strings.NewReader(`{"model":"jev-1.13.0","answers":{"q":{"type":"noul","noul":0.9}}}`), true, span)
	require.NoError(t, err)
	require.NotNil(t, span.recorded)
	require.Equal(t, "jev-1.13.0", span.recorded.Model)
	require.JSONEq(t, `{"type":"noul","noul":0.9}`, string(span.recorded.Answers["q"]))
}

func TestTypeSafeToTypeSafeTranslatorSystemOne_ResponseError(t *testing.T) {
	translator := NewSystemOneTypeSafeToTypeSafeTranslator("v1", "")

	for _, tc := range []struct {
		status, expType string
	}{
		{status: "401", expType: typesafeschema.ErrorTypeAuthentication},
		{status: "429", expType: typesafeschema.ErrorTypeAPIUsage},
		{status: "529", expType: typesafeschema.ErrorTypeAPI},
	} {
		t.Run("non_json_error_"+tc.status, func(t *testing.T) {
			respHeaders := map[string]string{statusHeaderName: tc.status, contentTypeHeaderName: "text/plain"}
			headerMutation, bodyMutation, err := translator.ResponseError(respHeaders, strings.NewReader("Overloaded"))
			require.NoError(t, err)
			require.Len(t, headerMutation, 2)
			var typesafeErr typesafeschema.SystemOneError
			require.NoError(t, json.Unmarshal(bodyMutation, &typesafeErr))
			require.Equal(t, tc.expType, typesafeErr.Detail.ErrorType)
			require.Equal(t, "Overloaded", typesafeErr.Detail.Message)
		})
	}

	t.Run("json_error_passthrough", func(t *testing.T) {
		respHeaders := map[string]string{statusHeaderName: "400", contentTypeHeaderName: jsonContentType}
		headerMutation, bodyMutation, err := translator.ResponseError(respHeaders, strings.NewReader(`{"detail":{"error_type":"api_usage_error","message":"Unknown model: no-such-model"}}`))
		require.NoError(t, err)
		require.Nil(t, headerMutation)
		require.Nil(t, bodyMutation)
	})

	t.Run("read_error", func(t *testing.T) {
		respHeaders := map[string]string{statusHeaderName: "500", contentTypeHeaderName: "text/plain"}
		_, _, err := translator.ResponseError(respHeaders, alwaysErrReader{})
		require.ErrorContains(t, err, "failed to read error body")
	})
}
