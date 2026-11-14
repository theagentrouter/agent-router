// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package gcp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

// fullBatchPredictionJob sets every BatchPredictionJob field, using the REST field names from the
// Vertex AI discovery document. A field with a wrong JSON name is dropped on decode and fails the
// round trip.
const fullBatchPredictionJob = `{
  "name": "projects/p/locations/us-central1/batchPredictionJobs/123",
  "displayName": "job",
  "model": "publishers/google/models/gemini-2.5-flash",
  "modelVersionId": "1",
  "unmanagedContainerModel": {"artifactUri": "gs://b/model", "containerSpec": {"imageUri": "img"}},
  "inputConfig": {
    "instancesFormat": "jsonl",
    "gcsSource": {"uris": ["gs://b/in.jsonl"]},
    "bigquerySource": {"inputUri": "bq://p.d.t"},
    "vertexMultimodalDatasetSource": {"datasetName": "ds"}
  },
  "instanceConfig": {"instanceType": "object", "keyField": "key", "includedFields": ["a"], "excludedFields": ["b"]},
  "modelParameters": {"temperature": 0.2},
  "outputConfig": {
    "predictionsFormat": "jsonl",
    "gcsDestination": {"outputUriPrefix": "gs://b/out/"},
    "bigqueryDestination": {"outputUri": "bq://p"},
    "vertexMultimodalDatasetDestination": {"bigqueryDestination": {"outputUri": "bq://p.d"}, "displayName": "out"}
  },
  "dedicatedResources": {
    "machineSpec": {
      "machineType": "n1-standard-4", "acceleratorType": "NVIDIA_TESLA_T4", "acceleratorCount": 1,
      "tpuTopology": "2x2", "gpuPartitionSize": "1g.10gb",
      "reservationAffinity": {"reservationAffinityType": "SPECIFIC_RESERVATION", "key": "k", "values": ["v"]}
    },
    "startingReplicaCount": 1,
    "maxReplicaCount": 3
  },
  "serviceAccount": "sa@p.iam.gserviceaccount.com",
  "manualBatchTuningParameters": {"batchSize": 16},
  "generateExplanation": true,
  "explanationSpec": {"parameters": {"topK": 3}},
  "outputInfo": {
    "gcsOutputDirectory": "gs://b/out/prediction-1",
    "bigqueryOutputDataset": "bq://p.ds",
    "bigqueryOutputTable": "predictions",
    "vertexMultimodalDatasetName": "projects/p/locations/l/datasets/9"
  },
  "state": "JOB_STATE_FAILED",
  "error": {"code": 3, "message": "bad input", "details": [{"@type": "type.googleapis.com/google.rpc.BadRequest"}]},
  "partialFailures": [{"code": 5, "message": "file not found"}],
  "resourcesConsumed": {"replicaHours": 1.5},
  "completionStats": {"successfulCount": "8", "failedCount": "2", "incompleteCount": "1", "successfulForecastPointCount": "4"},
  "createTime": "2025-01-01T00:00:00Z",
  "startTime": "2025-01-01T00:01:00Z",
  "endTime": "2025-01-01T00:10:00Z",
  "updateTime": "2025-01-01T00:10:00Z",
  "labels": {"team": "ml"},
  "encryptionSpec": {"kmsKeyName": "projects/p/locations/l/keyRings/r/cryptoKeys/k"},
  "satisfiesPzs": true,
  "satisfiesPzi": true,
  "disableContainerLogging": true
}`

func TestBatchPredictionJob_RoundTrip(t *testing.T) {
	var job BatchPredictionJob
	require.NoError(t, json.Unmarshal([]byte(fullBatchPredictionJob), &job))

	// Spot-check typed fields the gateway reads.
	require.Equal(t, JobStateFailed, job.State)
	require.Equal(t, []string{"gs://b/in.jsonl"}, job.InputConfig.GCSSource.URIs)
	require.Equal(t, "gs://b/out/prediction-1", job.OutputInfo.GCSOutputDirectory)
	require.Equal(t, int64(8), job.CompletionStats.SuccessfulCount)
	require.Equal(t, int32(3), *job.Error.Code)
	require.Len(t, job.Error.Details, 1)

	out, err := json.Marshal(&job)
	require.NoError(t, err)
	require.JSONEq(t, fullBatchPredictionJob, string(out))
}

func TestListBatchPredictionJobsResponse_RoundTrip(t *testing.T) {
	const page = `{"batchPredictionJobs":[{"name":"x/1","state":"JOB_STATE_RUNNING"}],"nextPageToken":"tok"}`
	var resp ListBatchPredictionJobsResponse
	require.NoError(t, json.Unmarshal([]byte(page), &resp))
	require.Len(t, resp.BatchPredictionJobs, 1)
	require.Equal(t, "tok", resp.NextPageToken)

	out, err := json.Marshal(&resp)
	require.NoError(t, err)
	require.JSONEq(t, page, string(out))
}
