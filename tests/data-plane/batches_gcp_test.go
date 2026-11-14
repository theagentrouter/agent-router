// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

//go:build test_extproc

package dataplane

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"sigs.k8s.io/yaml"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/idcodec"
	"github.com/envoyproxy/ai-gateway/internal/version"
)

const (
	// gcpBatchTestBackendName is the per_route_rule_backend_name set on the testupstream-gcp-batch
	// endpoint in envoy.yaml.
	gcpBatchTestBackendName = "test-ns/gcp-batch-backend/route/gcp-batch-route/rule/0/ref/0"
	// gcpBatchTestModel is the routing model matched by the GCP batch test route.
	gcpBatchTestModel = "gcp-batch-test-model"
	// gcpBatchTestJobsPath is the upstream Vertex AI batch jobs path built from the GCP auth
	// handler's project/region prefix and the translator's relative path.
	gcpBatchTestJobsPath = "/v1/projects/gcp-project-name/locations/gcp-region/batchPredictionJobs"
	// gcpBatchTestAuthHeader is the Authorization header the GCP auth handler must inject.
	gcpBatchTestAuthHeader = "Authorization:Bearer " + fakeGCPAuthToken
	// gcpBatchTestInputURI is the Cloud Storage batch input passed as input_file_id.
	gcpBatchTestInputURI = "gs://test-bucket/batch/input.jsonl"
)

// gcpBatchTestConfig builds the filter config with the GCP Vertex AI batch backend alongside the
// files backend, so both batch routes resolve.
func gcpBatchTestConfig() string {
	config := &filterapi.Config{
		Version: version.Parse(),
		Backends: []filterapi.Backend{
			{
				Name:   gcpBatchTestBackendName,
				Schema: gcpVertexAISchema,
				Auth: &filterapi.BackendAuth{GCPAuth: &filterapi.GCPAuth{
					AccessToken: fakeGCPAuthToken,
					Region:      "gcp-region",
					ProjectName: "gcp-project-name",
				}},
			},
			{
				Name:   filesTestBackendName1,
				Schema: openAISchema,
				Auth:   &filterapi.BackendAuth{APIKey: &filterapi.APIKeyAuth{Key: filesTestAPIKey}},
			},
		},
		Models: []filterapi.Model{{Name: gcpBatchTestModel}, {Name: filesTestModel}},
	}
	b, err := yaml.Marshal(config)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// gcpBatchJob returns a Vertex AI batch prediction job response body.
func gcpBatchJob(id, model, state, extra string) string {
	return `{"name":"projects/gcp-project-name/locations/gcp-region/batchPredictionJobs/` + id + `",` +
		`"displayName":"dp-test","model":"publishers/google/models/` + model + `",` +
		`"inputConfig":{"instancesFormat":"jsonl","gcsSource":{"uris":["` + gcpBatchTestInputURI + `"]}},` +
		`"outputConfig":{"predictionsFormat":"jsonl","gcsDestination":{"outputUriPrefix":"gs://test-bucket/out/"}},` +
		`"state":"` + state + `","createTime":"2025-01-01T00:00:00Z"` + extra + `}`
}

// requireGCPBatchID asserts that id is a gateway batch id for the GCP batch backend.
func requireGCPBatchID(t *testing.T, id, nativeID string) {
	t.Helper()
	d, err := newFilesTestCodec().Decode(id)
	require.NoError(t, err, "id %q", id)
	require.Equal(t, idcodec.BackendID{
		Namespace: "test-ns", Name: "gcp-batch-backend", Kind: idcodec.KindBatch, Model: gcpBatchTestModel, NativeID: nativeID,
	}, d)
}

// TestBatchAPI_GCPVertexAI walks the batch lifecycle against a GCP Vertex AI backend: create
// (chat completions and embeddings), retrieve, list with pagination, and cancel. It verifies the
// upstream path carries the GCP project/region prefix and token, that the request and response are
// translated between the OpenAI and Vertex AI shapes, and that gzip responses are decoded.
func TestBatchAPI_GCPVertexAI(t *testing.T) {
	env := startTestEnvironment(t, gcpBatchTestConfig(), true, false)
	addr := fmt.Sprintf("http://localhost:%d", env.EnvoyListenerPort())

	var batchID string
	for _, tc := range []struct {
		name, endpoint, nativeID string
	}{
		{"chat completions", "/v1/chat/completions", "1001"},
		{"embeddings", "/v1/embeddings", "1002"},
	} {
		t.Run("create/"+tc.name, func(t *testing.T) {
			created := doBatchRequest(t, addr, &batchRequest{
				method: http.MethodPost,
				path:   "/v1/batches",
				body: `{"input_file_id":"` + gcpBatchTestInputURI + `","endpoint":"` + tc.endpoint + `",` +
					`"completion_window":"24h","model":"` + gcpBatchTestModel + `",` +
					`"output_uri_prefix":"gs://test-bucket/out/","display_name":"dp-test","metadata":{"Team":"ML"}}`,
				expHeaders: gcpBatchTestAuthHeader,
				expPath:    gcpBatchTestJobsPath,
				expBody: `{"displayName":"dp-test","model":"publishers/google/models/` + gcpBatchTestModel + `",` +
					`"inputConfig":{"instancesFormat":"jsonl","gcsSource":{"uris":["` + gcpBatchTestInputURI + `"]}},` +
					`"outputConfig":{"predictionsFormat":"jsonl","gcsDestination":{"outputUriPrefix":"gs://test-bucket/out/"}},` +
					`"labels":{"team":"ml"}}`,
				responseType: "gzip",
				responseBody: gcpBatchJob(tc.nativeID, gcpBatchTestModel, "JOB_STATE_QUEUED", ""),
			}, http.StatusOK)
			requireGCPBatchID(t, gjson.GetBytes(created, "id").String(), tc.nativeID)
			require.Equal(t, "batch", gjson.GetBytes(created, "object").String())
			require.Equal(t, "validating", gjson.GetBytes(created, "status").String())
			require.Equal(t, gcpBatchTestInputURI, gjson.GetBytes(created, "input_file_id").String())
			require.Equal(t, gcpBatchTestModel, gjson.GetBytes(created, "model").String())
			require.Equal(t, int64(1735689600), gjson.GetBytes(created, "created_at").Int())
			require.NotContains(t, string(created), "projects/")
			if tc.nativeID == "1001" {
				batchID = gjson.GetBytes(created, "id").String()
			}
		})
	}
	require.NotEmpty(t, batchID)

	t.Run("retrieve", func(t *testing.T) {
		retrieved := doBatchRequest(t, addr, &batchRequest{
			method:       http.MethodGet,
			path:         "/v1/batches/" + batchID,
			expHeaders:   gcpBatchTestAuthHeader,
			expPath:      gcpBatchTestJobsPath + "/1001",
			responseType: "gzip",
			responseBody: gcpBatchJob("1001", gcpBatchTestModel, "JOB_STATE_SUCCEEDED",
				`,"outputInfo":{"gcsOutputDirectory":"gs://test-bucket/out/prediction-1"},`+
					`"completionStats":{"successfulCount":"2","failedCount":"1"},"endTime":"2025-01-01T00:10:00Z"`),
		}, http.StatusOK)
		require.Equal(t, batchID, gjson.GetBytes(retrieved, "id").String())
		require.Equal(t, "completed", gjson.GetBytes(retrieved, "status").String())
		require.Equal(t, "gs://test-bucket/out/prediction-1", gjson.GetBytes(retrieved, "output_file_id").String())
		require.Equal(t, int64(3), gjson.GetBytes(retrieved, "request_counts.total").Int())
		require.Equal(t, int64(2), gjson.GetBytes(retrieved, "request_counts.completed").Int())
		require.Equal(t, int64(1735690200), gjson.GetBytes(retrieved, "completed_at").Int())
	})

	t.Run("list and paginate", func(t *testing.T) {
		page := `{"batchPredictionJobs":[` +
			gcpBatchJob("1001", gcpBatchTestModel, "JOB_STATE_RUNNING", "") + `,` +
			gcpBatchJob("1003", "some-other-model", "JOB_STATE_RUNNING", "") +
			`],"nextPageToken":"page-token-2"}`
		listed := doBatchRequest(t, addr, &batchRequest{
			method:       http.MethodGet,
			path:         "/v1/batches?model=" + gcpBatchTestModel + "&limit=2",
			expHeaders:   gcpBatchTestAuthHeader,
			expPath:      gcpBatchTestJobsPath,
			expQuery:     "pageSize=2",
			responseBody: page,
		}, http.StatusOK)
		require.Equal(t, int64(1), gjson.GetBytes(listed, "data.#").Int(), "jobs for other models are filtered out")
		require.Equal(t, batchID, gjson.GetBytes(listed, "data.0.id").String())
		require.Equal(t, "in_progress", gjson.GetBytes(listed, "data.0.status").String())
		require.True(t, gjson.GetBytes(listed, "has_more").Bool())
		require.NotContains(t, string(listed), "page-token-2", "the page token must not leak")

		// The next-page cursor is last_id: the encrypted Vertex AI page token.
		cursor := gjson.GetBytes(listed, "last_id").String()
		requireGCPBatchID(t, cursor, "page-token-2")
		last := doBatchRequest(t, addr, &batchRequest{
			method:       http.MethodGet,
			path:         "/v1/batches?limit=2&after=" + cursor,
			expHeaders:   gcpBatchTestAuthHeader,
			expPath:      gcpBatchTestJobsPath,
			expQuery:     "pageSize=2&pageToken=page-token-2",
			responseBody: `{"batchPredictionJobs":[]}`,
		}, http.StatusOK)
		require.False(t, gjson.GetBytes(last, "has_more").Bool())
		require.Equal(t, int64(0), gjson.GetBytes(last, "data.#").Int())
	})

	t.Run("cancel", func(t *testing.T) {
		cancelled := doBatchRequest(t, addr, &batchRequest{
			method:       http.MethodPost,
			path:         "/v1/batches/" + batchID + "/cancel",
			expHeaders:   gcpBatchTestAuthHeader,
			expPath:      gcpBatchTestJobsPath + "/1001:cancel",
			expBody:      `{}`,
			responseBody: `{}`,
		}, http.StatusOK)
		require.Equal(t, batchID, gjson.GetBytes(cancelled, "id").String())
		require.Equal(t, "cancelling", gjson.GetBytes(cancelled, "status").String())
		require.NotZero(t, gjson.GetBytes(cancelled, "cancelling_at").Int())
	})
}

// TestBatchAPI_GCPVertexAIErrors covers GCP Vertex AI specific rejections and upstream errors.
func TestBatchAPI_GCPVertexAIErrors(t *testing.T) {
	env := startTestEnvironment(t, gcpBatchTestConfig(), true, false)
	addr := fmt.Sprintf("http://localhost:%d", env.EnvoyListenerPort())
	batchID, err := newFilesTestCodec().Encode(&idcodec.BackendID{
		Namespace: "test-ns", Name: "gcp-batch-backend", Kind: idcodec.KindBatch, Model: gcpBatchTestModel, NativeID: "1001",
	})
	require.NoError(t, err)
	create := func(fields string) string {
		return `{"input_file_id":"` + gcpBatchTestInputURI + `","completion_window":"24h"` + fields + `}`
	}

	for _, tc := range []struct {
		name      string
		req       batchRequest
		expStatus int
		expMsg    string
	}{
		{"create - storage URI without model", batchRequest{
			method: http.MethodPost, path: "/v1/batches",
			body: create(`,"endpoint":"/v1/chat/completions"`),
		}, http.StatusBadRequest, "'model' field is required"},
		{"create - unsupported endpoint", batchRequest{
			method: http.MethodPost, path: "/v1/batches",
			body: create(`,"endpoint":"/v1/responses","model":"` + gcpBatchTestModel + `"`),
		}, http.StatusBadRequest, "unsupported batch endpoint"},
		{"create - non-gs output prefix", batchRequest{
			method: http.MethodPost, path: "/v1/batches",
			body: create(`,"endpoint":"/v1/embeddings","model":"` + gcpBatchTestModel + `","output_uri_prefix":"s3://x/"`),
		}, http.StatusUnprocessableEntity, "output_uri_prefix"},
		{"retrieve - upstream error is translated", batchRequest{
			method: http.MethodGet, path: "/v1/batches/" + batchID,
			expHeaders: gcpBatchTestAuthHeader, expPath: gcpBatchTestJobsPath + "/1001",
			responseStatus: "404", responseType: "gzip",
			responseBody: `{"error":{"code":404,"message":"BatchPredictionJob does not exist.","status":"NOT_FOUND"}}`,
		}, http.StatusNotFound, "BatchPredictionJob does not exist."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := doBatchRequest(t, addr, &tc.req, tc.expStatus)
			require.Contains(t, string(body), tc.expMsg)
		})
	}

	t.Run("upstream error has the OpenAI shape", func(t *testing.T) {
		body := doBatchRequest(t, addr, &batchRequest{
			method: http.MethodGet, path: "/v1/batches/" + batchID,
			expHeaders: gcpBatchTestAuthHeader, expPath: gcpBatchTestJobsPath + "/1001",
			responseStatus: "404",
			responseBody:   `{"error":{"code":404,"message":"gone","status":"NOT_FOUND"}}`,
		}, http.StatusNotFound)
		require.Equal(t, "NOT_FOUND", gjson.GetBytes(body, "error.type").String())
		require.Equal(t, "gone", gjson.GetBytes(body, "error.message").String())
	})
}
