// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
)

func fixGCPBatchNow(t *testing.T, unix int64) {
	t.Helper()
	orig := gcpBatchNow
	gcpBatchNow = func() time.Time { return time.Unix(unix, 0) }
	t.Cleanup(func() { gcpBatchNow = orig })
}

func headerValue(hdrs []internalapi.Header, key string) string {
	for _, h := range hdrs {
		if h.Key() == key {
			return h.Value()
		}
	}
	return ""
}

func TestGCPVertexAIBatchCreate_RequestBody(t *testing.T) {
	fixGCPBatchNow(t, 1700000000)

	tests := []struct {
		name     string
		body     string
		req      BatchesRequest
		wantBody string
	}{
		{
			name: "chat completions with defaults",
			body: `{"input_file_id":"gs://bkt/dir/in.jsonl","endpoint":"/v1/chat/completions","completion_window":"24h","model":"gemini-2.5-flash","metadata":{"Team":"ML Ops","1st":"x"}}`,
			req:  BatchesRequest{Model: "gemini-2.5-flash"},
			wantBody: `{"displayName":"aigw-batch-1700000000","model":"publishers/google/models/gemini-2.5-flash",
				"inputConfig":{"instancesFormat":"jsonl","gcsSource":{"uris":["gs://bkt/dir/in.jsonl"]}},
				"outputConfig":{"predictionsFormat":"jsonl","gcsDestination":{"outputUriPrefix":"gs://bkt/dir/output/"}},
				"labels":{"team":"ml_ops","m_1st":"x"}}`,
		},
		{
			name: "embeddings with vendor fields",
			body: `{"input_file_id":"gs://bkt/in.jsonl","endpoint":"/v1/embeddings","completion_window":"24h","model":"text-embedding-005",
				"output_uri_prefix":"gs://out/p/","display_name":"my job","labels":{"env":"prod"},"metadata":{"ignored":"yes"}}`,
			req: BatchesRequest{Model: "text-embedding-005"},
			wantBody: `{"displayName":"my job","model":"publishers/google/models/text-embedding-005",
				"inputConfig":{"instancesFormat":"jsonl","gcsSource":{"uris":["gs://bkt/in.jsonl"]}},
				"outputConfig":{"predictionsFormat":"jsonl","gcsDestination":{"outputUriPrefix":"gs://out/p/"}},
				"labels":{"env":"prod"}}`,
		},
		{
			name: "model name override wins",
			body: `{"input_file_id":"gs://bkt/in.jsonl","endpoint":"/v1/chat/completions","model":"alias"}`,
			req:  BatchesRequest{Model: "alias", ModelNameOverride: "gemini-2.5-pro"},
			wantBody: `{"displayName":"aigw-batch-1700000000","model":"publishers/google/models/gemini-2.5-pro",
				"inputConfig":{"instancesFormat":"jsonl","gcsSource":{"uris":["gs://bkt/in.jsonl"]}},
				"outputConfig":{"predictionsFormat":"jsonl","gcsDestination":{"outputUriPrefix":"gs://bkt/output/"}}}`,
		},
		{
			name: "full resource model name kept",
			body: `{"input_file_id":"gs://bkt/in.jsonl","endpoint":"/v1/chat/completions","model":"projects/p/locations/l/models/123"}`,
			req:  BatchesRequest{},
			wantBody: `{"displayName":"aigw-batch-1700000000","model":"projects/p/locations/l/models/123",
				"inputConfig":{"instancesFormat":"jsonl","gcsSource":{"uris":["gs://bkt/in.jsonl"]}},
				"outputConfig":{"predictionsFormat":"jsonl","gcsDestination":{"outputUriPrefix":"gs://bkt/output/"}}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := &openAIToGCPVertexAIBatchCreateTranslator{}
			hdrs, body, err := tr.RequestBody([]byte(tc.body), &tc.req, false)
			require.NoError(t, err)
			require.Equal(t, "batchPredictionJobs", headerValue(hdrs, ":path"))
			require.JSONEq(t, tc.wantBody, string(body))
		})
	}
}

func TestGCPVertexAIBatchCreate_RequestBodyErrors(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr error
		msg     string
	}{
		{"malformed", `{`, internalapi.ErrMalformedRequest, "failed to parse"},
		{"unsupported endpoint", `{"input_file_id":"gs://b/i","endpoint":"/v1/responses","model":"m"}`, ErrUnsupportedBatchEndpoint, "/v1/responses"},
		{"not a gcs uri", `{"input_file_id":"file-abc","endpoint":"/v1/embeddings","model":"m"}`, internalapi.ErrInvalidRequestBody, "gs://"},
		{"missing model", `{"input_file_id":"gs://b/i","endpoint":"/v1/embeddings"}`, internalapi.ErrInvalidRequestBody, "'model'"},
		{"bad output prefix", `{"input_file_id":"gs://b/i","endpoint":"/v1/embeddings","model":"m","output_uri_prefix":"s3://x"}`, internalapi.ErrInvalidRequestBody, "output_uri_prefix"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := &openAIToGCPVertexAIBatchCreateTranslator{}
			_, _, err := tr.RequestBody([]byte(tc.body), &BatchesRequest{}, false)
			require.Error(t, err)
			require.ErrorIs(t, err, tc.wantErr)
			require.Contains(t, err.Error(), tc.msg)
		})
	}
}

func TestGCPVertexAIBatch_RetrieveAndCancelPaths(t *testing.T) {
	hdrs, body, err := (&openAIToGCPVertexAIBatchRetrieveTranslator{}).RequestBody(nil, &BatchesRequest{NativeID: "123"}, false)
	require.NoError(t, err)
	require.Nil(t, body)
	require.Equal(t, "batchPredictionJobs/123", headerValue(hdrs, ":path"))

	cancel := &openAIToGCPVertexAIBatchCancelTranslator{}
	hdrs, body, err = cancel.RequestBody(nil, &BatchesRequest{NativeID: "123", Model: "gemini-2.5-flash"}, false)
	require.NoError(t, err)
	require.Equal(t, "{}", string(body))
	require.Equal(t, "batchPredictionJobs/123:cancel", headerValue(hdrs, ":path"))
}

func TestGCPVertexAIBatchCancel_ResponseBody(t *testing.T) {
	fixGCPBatchNow(t, 1700000000)
	cancel := &openAIToGCPVertexAIBatchCancelTranslator{}
	_, _, err := cancel.RequestBody(nil, &BatchesRequest{NativeID: "123", Model: "text-embedding-005"}, false)
	require.NoError(t, err)
	hdrs, body, usage, model, err := cancel.ResponseBody(nil, strings.NewReader(`{}`), true, nil)
	require.NoError(t, err)
	require.Equal(t, metrics.TokenUsage{}, usage)
	require.Equal(t, internalapi.ResponseModel("text-embedding-005"), model)
	require.Equal(t, "application/json", headerValue(hdrs, "content-type"))
	require.JSONEq(t, `{"id":"123","object":"batch","endpoint":"/v1/embeddings","model":"text-embedding-005",
		"input_file_id":"","completion_window":"24h","status":"cancelling","created_at":0,"cancelling_at":1700000000}`, string(body))
}

func TestGCPVertexAIBatch_ResponseBody(t *testing.T) {
	const job = `{
		"name":"projects/p/locations/us-central1/batchPredictionJobs/456",
		"displayName":"job","model":"publishers/google/models/gemini-2.5-flash",
		"inputConfig":{"instancesFormat":"jsonl","gcsSource":{"uris":["gs://bkt/in.jsonl"]}},
		"outputConfig":{"predictionsFormat":"jsonl","gcsDestination":{"outputUriPrefix":"gs://bkt/out/"}},
		"outputInfo":{"gcsOutputDirectory":"gs://bkt/out/prediction-model-2025"},
		"state":"JOB_STATE_SUCCEEDED",
		"completionStats":{"successfulCount":"8","failedCount":"2","incompleteCount":"1"},
		"createTime":"2023-11-14T22:13:20Z","startTime":"2023-11-14T22:13:30.5Z","endTime":"2023-11-14T22:23:20Z",
		"labels":{"team":"ml"}
	}`
	for name, tr := range map[string]BatchesTranslator{
		"create":   &openAIToGCPVertexAIBatchCreateTranslator{},
		"retrieve": &openAIToGCPVertexAIBatchRetrieveTranslator{},
	} {
		t.Run(name, func(t *testing.T) {
			_, body, _, model, err := tr.ResponseBody(nil, strings.NewReader(job), true, nil)
			require.NoError(t, err)
			require.Equal(t, internalapi.ResponseModel("gemini-2.5-flash"), model)
			require.JSONEq(t, `{"id":"456","object":"batch","endpoint":"/v1/chat/completions","model":"gemini-2.5-flash",
				"input_file_id":"gs://bkt/in.jsonl","completion_window":"24h","status":"completed",
				"output_file_id":"gs://bkt/out/prediction-model-2025",
				"created_at":1700000000,"in_progress_at":1700000010,"completed_at":1700000600,
				"request_counts":{"total":11,"completed":8,"failed":2},"metadata":{"team":"ml"}}`, string(body))
		})
	}

	t.Run("failed job with typed error details", func(t *testing.T) {
		_, body, _, _, err := (&openAIToGCPVertexAIBatchRetrieveTranslator{}).ResponseBody(nil, strings.NewReader(
			`{"name":"x/9","state":"JOB_STATE_FAILED","error":{"code":3,"message":"bad input",
			"details":[{"@type":"type.googleapis.com/google.rpc.BadRequest","fieldViolations":[]}]}}`), true, nil)
		require.NoError(t, err)
		require.Contains(t, string(body), `"errors":{"object":"list","data":[{"code":"3","message":"bad input"}]}`)
	})
	t.Run("invalid json", func(t *testing.T) {
		_, _, _, _, err := (&openAIToGCPVertexAIBatchRetrieveTranslator{}).ResponseBody(nil, strings.NewReader(`{`), true, nil)
		require.Error(t, err)
	})
	t.Run("missing name", func(t *testing.T) {
		_, _, _, _, err := (&openAIToGCPVertexAIBatchRetrieveTranslator{}).ResponseBody(nil, strings.NewReader(`{"state":"JOB_STATE_RUNNING"}`), true, nil)
		require.ErrorContains(t, err, "missing name")
	})
	t.Run("bad timestamp", func(t *testing.T) {
		_, _, _, _, err := (&openAIToGCPVertexAIBatchRetrieveTranslator{}).ResponseBody(nil,
			strings.NewReader(`{"name":"x/1","createTime":"yesterday"}`), true, nil)
		require.ErrorContains(t, err, "invalid timestamp")
	})
}

func TestGCPBatchPredictionJobToOpenAI_States(t *testing.T) {
	const end = "2023-11-14T22:23:20Z"
	code := int32(3)
	tests := []struct {
		state  gcp.JobState
		status openai.BatchStatus
		check  func(t *testing.T, b *openai.Batch)
	}{
		{gcp.JobStateQueued, openai.BatchStatusValidating, nil},
		{gcp.JobStatePending, openai.BatchStatusValidating, nil},
		{gcp.JobStateUnspecified, openai.BatchStatusValidating, nil},
		{gcp.JobStateRunning, openai.BatchStatusInProgress, nil},
		{gcp.JobStateUpdating, openai.BatchStatusInProgress, nil},
		{gcp.JobStatePaused, openai.BatchStatusInProgress, nil},
		{gcp.JobStatePartiallySucceeded, openai.BatchStatusCompleted, func(t *testing.T, b *openai.Batch) { require.NotNil(t, b.CompletedAt) }},
		{gcp.JobStateFailed, openai.BatchStatusFailed, func(t *testing.T, b *openai.Batch) {
			require.NotNil(t, b.FailedAt)
			require.Equal(t, &openai.BatchErrors{Object: "list", Data: []openai.BatchError{{Code: "3", Message: "bad input"}}}, b.Errors)
		}},
		{gcp.JobStateCancelling, openai.BatchStatusCancelling, nil},
		{gcp.JobStateCancelled, openai.BatchStatusCancelled, func(t *testing.T, b *openai.Batch) { require.NotNil(t, b.CancelledAt) }},
		{gcp.JobStateExpired, openai.BatchStatusExpired, func(t *testing.T, b *openai.Batch) { require.NotNil(t, b.ExpiredAt) }},
	}
	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			job := &gcp.BatchPredictionJob{Name: "projects/p/locations/l/batchPredictionJobs/1", State: tc.state, EndTime: end}
			if tc.state == gcp.JobStateFailed {
				job.Error = &gcp.RPCStatus{Code: &code, Message: "bad input"}
			}
			b, err := gcpBatchPredictionJobToOpenAI(job)
			require.NoError(t, err)
			require.Equal(t, tc.status, b.Status)
			if tc.check != nil {
				tc.check(t, b)
			}
		})
	}
}

func TestGCPVertexAIBatchList(t *testing.T) {
	t.Run("request", func(t *testing.T) {
		tr := &openAIToGCPVertexAIBatchListTranslator{}
		hdrs, body, err := tr.RequestBody(nil, &BatchesRequest{Model: "gemini-2.5-flash", Query: "?limit=5&after=tok%2Fabc"}, false)
		require.NoError(t, err)
		require.Nil(t, body)
		require.Equal(t, "batchPredictionJobs?pageSize=5&pageToken=tok%2Fabc", headerValue(hdrs, ":path"))

		hdrs, _, err = tr.RequestBody(nil, &BatchesRequest{Model: "m"}, false)
		require.NoError(t, err)
		require.Equal(t, "batchPredictionJobs", headerValue(hdrs, ":path"))
	})
	t.Run("numeric after is rejected", func(t *testing.T) {
		_, _, err := (&openAIToGCPVertexAIBatchListTranslator{}).RequestBody(nil, &BatchesRequest{Model: "m", Query: "?after=123"}, false)
		require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
	})
	t.Run("malformed query", func(t *testing.T) {
		_, _, err := (&openAIToGCPVertexAIBatchListTranslator{}).RequestBody(nil, &BatchesRequest{Model: "m", Query: "?%zz"}, false)
		require.ErrorIs(t, err, internalapi.ErrMalformedRequest)
	})

	const page = `{"batchPredictionJobs":[
		{"name":"projects/p/locations/l/batchPredictionJobs/1","model":"publishers/google/models/gemini-2.5-flash","state":"JOB_STATE_RUNNING"},
		{"name":"projects/p/locations/l/batchPredictionJobs/2","model":"publishers/google/models/other","state":"JOB_STATE_RUNNING"},
		{"name":"projects/p/locations/l/batchPredictionJobs/3","model":"publishers/google/models/gemini-2.5-flash","state":"JOB_STATE_QUEUED"}
	],"nextPageToken":"next-tok"}`
	t.Run("response filters by model and returns page token", func(t *testing.T) {
		tr := &openAIToGCPVertexAIBatchListTranslator{}
		_, _, err := tr.RequestBody(nil, &BatchesRequest{Model: "gemini-2.5-flash"}, false)
		require.NoError(t, err)
		_, body, _, _, err := tr.ResponseBody(nil, strings.NewReader(page), true, nil)
		require.NoError(t, err)
		var got openai.BatchListResponse
		require.NoError(t, json.Unmarshal(body, &got))
		require.Len(t, got.Data, 2)
		require.Equal(t, "1", got.FirstID)
		require.Equal(t, "next-tok", got.LastID)
		require.True(t, got.HasMore)
	})
	t.Run("last page", func(t *testing.T) {
		tr := &openAIToGCPVertexAIBatchListTranslator{model: "gemini-2.5-flash"}
		_, body, _, _, err := tr.ResponseBody(nil, strings.NewReader(`{"batchPredictionJobs":[{"name":"x/7","model":"publishers/google/models/gemini-2.5-flash"}]}`), true, nil)
		require.NoError(t, err)
		require.JSONEq(t, `{"object":"list","has_more":false,"first_id":"7","last_id":"7","data":[{"id":"7","object":"batch",
			"endpoint":"/v1/chat/completions","model":"gemini-2.5-flash","input_file_id":"","completion_window":"24h",
			"status":"validating","created_at":0}]}`, string(body))
	})
	t.Run("empty page", func(t *testing.T) {
		tr := &openAIToGCPVertexAIBatchListTranslator{model: "m"}
		_, body, _, _, err := tr.ResponseBody(nil, strings.NewReader(`{}`), true, nil)
		require.NoError(t, err)
		require.JSONEq(t, `{"object":"list","has_more":false,"data":[]}`, string(body))
	})
	t.Run("invalid json", func(t *testing.T) {
		_, _, _, _, err := (&openAIToGCPVertexAIBatchListTranslator{}).ResponseBody(nil, strings.NewReader(`[`), true, nil)
		require.Error(t, err)
	})
}

func TestGCPVertexAIBatch_ResponseError(t *testing.T) {
	hdrs, body, err := (&openAIToGCPVertexAIBatchRetrieveTranslator{}).ResponseError(
		map[string]string{":status": "404"},
		bytes.NewReader([]byte(`{"error":{"code":404,"message":"job not found","status":"NOT_FOUND"}}`)))
	require.NoError(t, err)
	require.Equal(t, "application/json", headerValue(hdrs, "content-type"))
	require.JSONEq(t, `{"type":"error","error":{"type":"NOT_FOUND","message":"job not found","code":"404"}}`, string(body))
}

func TestSanitizeGCPLabels(t *testing.T) {
	require.Nil(t, sanitizeGCPLabels(nil))
	got := sanitizeGCPLabels(map[string]string{"Team": "ML Ops", "9lives": "Cat", "!!!": "x", "": "y", "k": strings.Repeat("a", 100)})
	require.Equal(t, map[string]string{"team": "ml_ops", "m_9lives": "cat", "m____": "x", "k": strings.Repeat("a", 63)}, got)

	many := map[string]string{}
	for i := range 100 {
		many["k"+strings.Repeat("x", i%10)+string(rune('a'+i%26))+string(rune('a'+i/26))] = "v"
	}
	require.Len(t, sanitizeGCPLabels(many), 64)
}

func TestDefaultGCSOutputPrefix(t *testing.T) {
	require.Equal(t, "gs://b/d/output/", defaultGCSOutputPrefix("gs://b/d/in.jsonl"))
	require.Equal(t, "gs://b/output/", defaultGCSOutputPrefix("gs://b"))
}
