// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package gcp

import "github.com/envoyproxy/ai-gateway/internal/json"

// Vertex AI REST wire types for batch prediction jobs.
//
// These are the REST shapes of the projects.locations.batchPredictionJobs resource. They differ
// from the go-genai SDK types (BatchJob with src/dest), which the SDK converts before sending.
// https://cloud.google.com/vertex-ai/docs/reference/rest/v1/projects.locations.batchPredictionJobs

// JobState is the state of a Vertex AI job.
// https://cloud.google.com/vertex-ai/docs/reference/rest/v1/JobState
type JobState string

// Vertex AI job states.
const (
	JobStateUnspecified        JobState = "JOB_STATE_UNSPECIFIED"
	JobStateQueued             JobState = "JOB_STATE_QUEUED"
	JobStatePending            JobState = "JOB_STATE_PENDING"
	JobStateRunning            JobState = "JOB_STATE_RUNNING"
	JobStateSucceeded          JobState = "JOB_STATE_SUCCEEDED"
	JobStateFailed             JobState = "JOB_STATE_FAILED"
	JobStateCancelling         JobState = "JOB_STATE_CANCELLING"
	JobStateCancelled          JobState = "JOB_STATE_CANCELLED"
	JobStatePaused             JobState = "JOB_STATE_PAUSED"
	JobStateExpired            JobState = "JOB_STATE_EXPIRED"
	JobStateUpdating           JobState = "JOB_STATE_UPDATING"
	JobStatePartiallySucceeded JobState = "JOB_STATE_PARTIALLY_SUCCEEDED"
)

// BatchPredictionJob is the Vertex AI batchPredictionJobs resource.
// https://cloud.google.com/vertex-ai/docs/reference/rest/v1/projects.locations.batchPredictionJobs#BatchPredictionJob
//
// Fields that only apply to custom-trained or AutoML models (explanations, unmanaged container
// models) are kept as raw JSON: the gateway never reads them, but must not lose them.
type BatchPredictionJob struct {
	// Output only. Resource name: projects/{project}/locations/{location}/batchPredictionJobs/{id}.
	Name string `json:"name,omitempty"`
	// Required. The user-defined name of this job.
	DisplayName string `json:"displayName,omitempty"`
	// The model resource name, e.g. "publishers/google/models/gemini-2.5-flash". Exactly one of
	// Model and UnmanagedContainerModel must be set.
	Model string `json:"model,omitempty"`
	// Output only. The version id of the model that produces the predictions.
	ModelVersionID string `json:"modelVersionId,omitempty"`
	// Contents of the model, for a job that is not based on a Vertex AI Model resource.
	UnmanagedContainerModel json.RawMessage `json:"unmanagedContainerModel,omitempty"`
	// Required. Input configuration.
	InputConfig *BatchInputConfig `json:"inputConfig,omitempty"`
	// How input instances are converted to the prediction instances sent to the model.
	InstanceConfig *BatchInstanceConfig `json:"instanceConfig,omitempty"`
	// The parameters that govern the predictions; the schema depends on the model.
	ModelParameters json.RawMessage `json:"modelParameters,omitempty"`
	// Required. Output configuration.
	OutputConfig *BatchOutputConfig `json:"outputConfig,omitempty"`
	// The machine resources the job runs on. Not used by Gemini (publisher) models.
	DedicatedResources *BatchDedicatedResources `json:"dedicatedResources,omitempty"`
	// The service account that the model's container runs as.
	ServiceAccount string `json:"serviceAccount,omitempty"`
	// Tuning parameters for the batch size of each prediction call.
	ManualBatchTuningParameters *ManualBatchTuningParameters `json:"manualBatchTuningParameters,omitempty"`
	// Whether to generate explanations with the predictions.
	GenerateExplanation *bool `json:"generateExplanation,omitempty"`
	// Explanation configuration, used with GenerateExplanation.
	ExplanationSpec json.RawMessage `json:"explanationSpec,omitempty"`
	// Output only. Where the output was written.
	OutputInfo *BatchOutputInfo `json:"outputInfo,omitempty"`
	// Output only. The job state.
	State JobState `json:"state,omitempty"`
	// Output only. Only populated when the job state is JOB_STATE_FAILED or JOB_STATE_CANCELLED.
	Error *RPCStatus `json:"error,omitempty"`
	// Output only. Partial failures, such as single input files that could not be read. At most
	// 20 entries.
	PartialFailures []RPCStatus `json:"partialFailures,omitempty"`
	// Output only. Resources consumed by the job.
	ResourcesConsumed *ResourcesConsumed `json:"resourcesConsumed,omitempty"`
	// Output only. Statistics on completed and failed prediction instances.
	CompletionStats *CompletionStats `json:"completionStats,omitempty"`
	// Output only. RFC3339 timestamps of the job lifecycle.
	CreateTime string `json:"createTime,omitempty"`
	StartTime  string `json:"startTime,omitempty"`
	EndTime    string `json:"endTime,omitempty"`
	UpdateTime string `json:"updateTime,omitempty"`
	// User-defined labels.
	Labels map[string]string `json:"labels,omitempty"`
	// Customer-managed encryption key for the job.
	EncryptionSpec *EncryptionSpec `json:"encryptionSpec,omitempty"`
	// Output only. Reserved for future use.
	SatisfiesPzs bool `json:"satisfiesPzs,omitempty"`
	// Output only. Reserved for future use.
	SatisfiesPzi bool `json:"satisfiesPzi,omitempty"`
	// Whether to disable sending container stdout/stderr to Cloud Logging. Only for custom-trained
	// and AutoML tabular models.
	DisableContainerLogging bool `json:"disableContainerLogging,omitempty"`
}

// RPCStatus is a google.rpc.Status, used for job errors and partial failures. Details are typed
// objects ({"@type": ...}), so they are kept raw.
// https://cloud.google.com/vertex-ai/docs/reference/rest/Shared.Types/Status
type RPCStatus struct {
	Code    *int32            `json:"code,omitempty"`
	Message string            `json:"message,omitempty"`
	Details []json.RawMessage `json:"details,omitempty"`
}

// CompletionStats holds success and error statistics of a batch prediction job. The REST API
// encodes the int64 counts as JSON strings.
// https://cloud.google.com/vertex-ai/docs/reference/rest/v1/CompletionStats
type CompletionStats struct {
	// Output only. The number of entities that had been processed successfully.
	SuccessfulCount int64 `json:"successfulCount,omitempty,string"`
	// Output only. The number of entities for which any error was encountered.
	FailedCount int64 `json:"failedCount,omitempty,string"`
	// Output only. The number of entities whose processing had not finished (either in a
	// successful or failed state). Set to -1 if the number is unknown.
	IncompleteCount int64 `json:"incompleteCount,omitempty,string"`
	// Output only. The number of successful forecast points; only for forecasting jobs.
	SuccessfulForecastPointCount int64 `json:"successfulForecastPointCount,omitempty,string"`
}

// ResourcesConsumed describes the resources consumed by a job.
type ResourcesConsumed struct {
	// Output only. The number of replica hours used.
	ReplicaHours float64 `json:"replicaHours,omitempty"`
}

// BatchInputConfig configures the input of a batch prediction job. Exactly one source is set.
type BatchInputConfig struct {
	// Required. The format of the instances, e.g. "jsonl" or "bigquery".
	InstancesFormat string `json:"instancesFormat,omitempty"`
	// Cloud Storage input location.
	GCSSource *GCSSource `json:"gcsSource,omitempty"`
	// BigQuery input location.
	BigQuerySource *BigQuerySource `json:"bigquerySource,omitempty"`
	// Vertex AI multimodal dataset input.
	VertexMultimodalDatasetSource *VertexMultimodalDatasetSource `json:"vertexMultimodalDatasetSource,omitempty"`
}

// GCSSource is a Cloud Storage input location.
type GCSSource struct {
	// Required. Cloud Storage URIs to the input files; wildcards are allowed.
	URIs []string `json:"uris,omitempty"`
}

// BigQuerySource is a BigQuery input location.
type BigQuerySource struct {
	// Required. BigQuery URI to a table, e.g. "bq://projectId.bqDatasetId.bqTableId".
	InputURI string `json:"inputUri,omitempty"`
}

// VertexMultimodalDatasetSource is a Vertex AI multimodal dataset input.
type VertexMultimodalDatasetSource struct {
	// Required. The dataset resource name.
	DatasetName string `json:"datasetName,omitempty"`
}

// BatchInstanceConfig configures how input instances are converted to prediction instances.
type BatchInstanceConfig struct {
	// The instance format the model accepts: "object" or "array".
	InstanceType string `json:"instanceType,omitempty"`
	// The field whose value identifies each instance in the output.
	KeyField string `json:"keyField,omitempty"`
	// Fields to include in the prediction instances; exclusive with ExcludedFields.
	IncludedFields []string `json:"includedFields,omitempty"`
	// Fields to exclude from the prediction instances; exclusive with IncludedFields.
	ExcludedFields []string `json:"excludedFields,omitempty"`
}

// BatchOutputConfig configures the output of a batch prediction job. Exactly one destination is
// set.
type BatchOutputConfig struct {
	// Required. The format of the predictions, e.g. "jsonl" or "bigquery".
	PredictionsFormat string `json:"predictionsFormat,omitempty"`
	// Cloud Storage output location.
	GCSDestination *GCSDestination `json:"gcsDestination,omitempty"`
	// BigQuery output location.
	BigQueryDestination *BigQueryDestination `json:"bigqueryDestination,omitempty"`
	// Vertex AI multimodal dataset output.
	VertexMultimodalDatasetDestination *VertexMultimodalDatasetDestination `json:"vertexMultimodalDatasetDestination,omitempty"`
}

// GCSDestination is a Cloud Storage output location.
type GCSDestination struct {
	// Required. Cloud Storage URI prefix under which the output is written.
	OutputURIPrefix string `json:"outputUriPrefix,omitempty"`
}

// BigQueryDestination is a BigQuery output location.
type BigQueryDestination struct {
	// Required. BigQuery URI to a project or table, e.g. "bq://projectId".
	OutputURI string `json:"outputUri,omitempty"`
}

// VertexMultimodalDatasetDestination is a Vertex AI multimodal dataset output.
type VertexMultimodalDatasetDestination struct {
	// BigQuery destination for the dataset's underlying table.
	BigQueryDestination *BigQueryDestination `json:"bigqueryDestination,omitempty"`
	// Display name of the output dataset.
	DisplayName string `json:"displayName,omitempty"`
}

// BatchOutputInfo describes where the output of a batch prediction job was written.
type BatchOutputInfo struct {
	// Output only. The full path of the Cloud Storage directory created for the output.
	GCSOutputDirectory string `json:"gcsOutputDirectory,omitempty"`
	// Output only. The BigQuery dataset created for the output.
	BigQueryOutputDataset string `json:"bigqueryOutputDataset,omitempty"`
	// Output only. The name of the BigQuery table created for the output.
	BigQueryOutputTable string `json:"bigqueryOutputTable,omitempty"`
	// Output only. The resource name of the Vertex AI multimodal dataset created for the output.
	VertexMultimodalDatasetName string `json:"vertexMultimodalDatasetName,omitempty"`
}

// BatchDedicatedResources describes the machines a batch prediction job runs on.
type BatchDedicatedResources struct {
	// Required. The machine type, accelerators, and reservation.
	MachineSpec *MachineSpec `json:"machineSpec,omitempty"`
	// The number of machines started with.
	StartingReplicaCount int32 `json:"startingReplicaCount,omitempty"`
	// The maximum number of machines the job may scale to.
	MaxReplicaCount int32 `json:"maxReplicaCount,omitempty"`
}

// MachineSpec is the specification of a single machine.
type MachineSpec struct {
	MachineType         string               `json:"machineType,omitempty"`
	AcceleratorType     string               `json:"acceleratorType,omitempty"`
	AcceleratorCount    int32                `json:"acceleratorCount,omitempty"`
	TPUTopology         string               `json:"tpuTopology,omitempty"`
	GPUPartitionSize    string               `json:"gpuPartitionSize,omitempty"`
	ReservationAffinity *ReservationAffinity `json:"reservationAffinity,omitempty"`
}

// ReservationAffinity selects the Compute Engine reservations to consume.
type ReservationAffinity struct {
	ReservationAffinityType string   `json:"reservationAffinityType,omitempty"`
	Key                     string   `json:"key,omitempty"`
	Values                  []string `json:"values,omitempty"`
}

// ManualBatchTuningParameters tunes the batch size of each prediction call.
type ManualBatchTuningParameters struct {
	// The number of records in each batch.
	BatchSize int32 `json:"batchSize,omitempty"`
}

// EncryptionSpec is a customer-managed encryption key configuration.
type EncryptionSpec struct {
	// Required. The Cloud KMS resource identifier of the key.
	KMSKeyName string `json:"kmsKeyName,omitempty"`
}

// ListBatchPredictionJobsResponse is the response of batchPredictionJobs.list.
type ListBatchPredictionJobsResponse struct {
	// The jobs on this page.
	BatchPredictionJobs []BatchPredictionJob `json:"batchPredictionJobs,omitempty"`
	// Token to retrieve the next page; empty when there are no more pages.
	NextPageToken string `json:"nextPageToken,omitempty"`
}
