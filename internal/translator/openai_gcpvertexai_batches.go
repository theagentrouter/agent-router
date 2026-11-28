// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
)

const (
	// gcpBatchPredictionJobsPath is the Vertex AI batch prediction jobs collection, relative to
	// "/v1/projects/{project}/locations/{region}/". The GCP backend auth handler adds the prefix.
	gcpBatchPredictionJobsPath = "batchPredictionJobs"
	// gcsURIScheme is the scheme of Cloud Storage URIs accepted as batch input.
	gcsURIScheme = "gs://"
	// gcpBatchFormatJSONL is the Vertex AI batch instances/predictions format used by the gateway.
	gcpBatchFormatJSONL = "jsonl"
	// gcpModelResourcePrefix prefixes a Google publisher model in a Vertex AI resource name.
	gcpModelResourcePrefix = "publishers/" + gcpModelPublisherGoogle + "/models/"
	// gcpBatchDefaultDisplayNamePrefix is the display name prefix used when none is given.
	gcpBatchDefaultDisplayNamePrefix = "aigw-batch-"
	// gcpMaxLabels and gcpMaxLabelLength are the Vertex AI label limits.
	gcpMaxLabels      = 64
	gcpMaxLabelLength = 63
)

// gcpBatchNow returns the current time; it is a variable so tests can make it deterministic.
var gcpBatchNow = time.Now

// openAIToGCPVertexAIBatchCreateTranslator translates POST /v1/batches to
// POST batchPredictionJobs. The batch input must be a Cloud Storage URI ("gs://...") holding
// Vertex AI batch JSONL; the gateway does not rewrite the input file.
type openAIToGCPVertexAIBatchCreateTranslator struct {
	gcpVertexAIBatchResponse
}

// openAIToGCPVertexAIBatchRetrieveTranslator translates GET /v1/batches/{id} to
// GET batchPredictionJobs/{id}.
type openAIToGCPVertexAIBatchRetrieveTranslator struct {
	gcpVertexAIBatchResponse
}

// openAIToGCPVertexAIBatchCancelTranslator translates POST /v1/batches/{id}/cancel to
// POST batchPredictionJobs/{id}:cancel. Vertex AI answers a cancel with an empty object, so the
// translator synthesizes a "cancelling" batch.
type openAIToGCPVertexAIBatchCancelTranslator struct {
	gcpVertexAIBatchErrors
	nativeID string
	model    string
}

// openAIToGCPVertexAIBatchListTranslator translates GET /v1/batches to GET batchPredictionJobs.
// The Vertex AI page token is returned as last_id (the processor encrypts it into a gateway batch
// id) and is sent back as pageToken when the client passes it as "after".
type openAIToGCPVertexAIBatchListTranslator struct {
	gcpVertexAIBatchErrors
	model string
}

var (
	_ BatchesTranslator = (*openAIToGCPVertexAIBatchCreateTranslator)(nil)
	_ BatchesTranslator = (*openAIToGCPVertexAIBatchRetrieveTranslator)(nil)
	_ BatchesTranslator = (*openAIToGCPVertexAIBatchCancelTranslator)(nil)
	_ BatchesTranslator = (*openAIToGCPVertexAIBatchListTranslator)(nil)
)

// gcpVertexAIBatchErrors implements the response header and error methods shared by every Vertex
// AI batch translator.
type gcpVertexAIBatchErrors struct{}

// ResponseHeaders implements [Translator.ResponseHeaders].
func (gcpVertexAIBatchErrors) ResponseHeaders(map[string]string) ([]internalapi.Header, error) {
	return nil, nil
}

// ResponseError implements [Translator.ResponseError] by converting the Vertex AI error envelope
// to the OpenAI one.
func (gcpVertexAIBatchErrors) ResponseError(respHeaders map[string]string, body io.Reader) (
	newHeaders []internalapi.Header, mutatedBody []byte, err error,
) {
	return convertGCPVertexAIErrorToOpenAI(respHeaders, body)
}

// gcpVertexAIBatchResponse converts a single BatchPredictionJob response to an OpenAI batch.
type gcpVertexAIBatchResponse struct {
	gcpVertexAIBatchErrors
}

// ResponseBody implements [Translator.ResponseBody].
func (gcpVertexAIBatchResponse) ResponseBody(_ map[string]string, body io.Reader, _ bool, _ any) (
	newHeaders []internalapi.Header, mutatedBody []byte, tokenUsage metrics.TokenUsage,
	responseModel internalapi.ResponseModel, err error,
) {
	var job gcp.BatchPredictionJob
	if err = json.NewDecoder(body).Decode(&job); err != nil {
		return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("failed to decode Vertex AI batch prediction job: %w", err)
	}
	batch, err := gcpBatchPredictionJobToOpenAI(&job)
	if err != nil {
		return nil, nil, metrics.TokenUsage{}, "", err
	}
	mutatedBody, err = json.Marshal(batch)
	if err != nil {
		return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("failed to encode batch: %w", err)
	}
	return jsonBodyHeaders(mutatedBody), mutatedBody, metrics.TokenUsage{}, batch.Model, nil
}

// RequestBody implements [Translator.RequestBody].
func (t *openAIToGCPVertexAIBatchCreateTranslator) RequestBody(raw []byte, req *BatchesRequest, _ bool) (
	newHeaders []internalapi.Header, mutatedBody []byte, err error,
) {
	var params openai.BatchNewParams
	if err = json.Unmarshal(raw, &params); err != nil {
		return nil, nil, fmt.Errorf("%w: failed to parse batch create request: %w", internalapi.ErrMalformedRequest, err)
	}
	switch params.Endpoint {
	case openai.BatchNewParamsEndpointV1ChatCompletions, openai.BatchNewParamsEndpointV1Embeddings:
	default:
		return nil, nil, fmt.Errorf("%w: GCP Vertex AI batches support only %q and %q, got %q",
			ErrUnsupportedBatchEndpoint, openai.BatchNewParamsEndpointV1ChatCompletions,
			openai.BatchNewParamsEndpointV1Embeddings, params.Endpoint)
	}
	if !strings.HasPrefix(params.InputFileID, gcsURIScheme) {
		return nil, nil, fmt.Errorf("%w: GCP Vertex AI batches require input_file_id to be a Cloud Storage URI (gs://...)",
			internalapi.ErrInvalidRequestBody)
	}

	model := req.ModelNameOverride
	if model == "" {
		model = params.Model
	}
	if model == "" {
		model = req.Model
	}
	if model == "" {
		return nil, nil, fmt.Errorf("%w: the 'model' field is required for GCP Vertex AI batches", internalapi.ErrInvalidRequestBody)
	}

	var vendor openai.GCPVertexAIBatchVendorFields
	if params.GCPVertexAIBatchVendorFields != nil {
		vendor = *params.GCPVertexAIBatchVendorFields
	}
	outputPrefix := vendor.OutputURIPrefix
	if outputPrefix == "" {
		outputPrefix = defaultGCSOutputPrefix(params.InputFileID)
	} else if !strings.HasPrefix(outputPrefix, gcsURIScheme) {
		return nil, nil, fmt.Errorf("%w: output_uri_prefix must be a Cloud Storage URI (gs://...)", internalapi.ErrInvalidRequestBody)
	}
	displayName := vendor.DisplayName
	if displayName == "" {
		displayName = gcpBatchDefaultDisplayNamePrefix + strconv.FormatInt(gcpBatchNow().Unix(), 10)
	}
	labels := vendor.Labels
	if labels == nil {
		labels = params.Metadata
	}

	job := gcp.BatchPredictionJob{
		DisplayName: displayName,
		Model:       gcpModelResourceName(model),
		InputConfig: &gcp.BatchInputConfig{
			InstancesFormat: gcpBatchFormatJSONL,
			GCSSource:       &gcp.GCSSource{URIs: []string{params.InputFileID}},
		},
		OutputConfig: &gcp.BatchOutputConfig{
			PredictionsFormat: gcpBatchFormatJSONL,
			GCSDestination:    &gcp.GCSDestination{OutputURIPrefix: outputPrefix},
		},
		Labels: sanitizeGCPLabels(labels),
	}
	mutatedBody, err = json.Marshal(&job)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to encode Vertex AI batch prediction job: %w", err)
	}
	return []internalapi.Header{{pathHeaderName, gcpBatchPredictionJobsPath}}, mutatedBody, nil
}

// RequestBody implements [Translator.RequestBody].
func (t *openAIToGCPVertexAIBatchRetrieveTranslator) RequestBody(_ []byte, req *BatchesRequest, _ bool) (
	newHeaders []internalapi.Header, mutatedBody []byte, err error,
) {
	return []internalapi.Header{{pathHeaderName, gcpBatchPredictionJobsPath + "/" + url.PathEscape(req.NativeID)}}, nil, nil
}

// RequestBody implements [Translator.RequestBody].
func (t *openAIToGCPVertexAIBatchCancelTranslator) RequestBody(_ []byte, req *BatchesRequest, _ bool) (
	newHeaders []internalapi.Header, mutatedBody []byte, err error,
) {
	t.nativeID = req.NativeID
	t.model = req.Model
	path := gcpBatchPredictionJobsPath + "/" + url.PathEscape(req.NativeID) + ":cancel"
	return []internalapi.Header{{pathHeaderName, path}}, []byte("{}"), nil
}

// ResponseBody implements [Translator.ResponseBody]. Vertex AI answers a successful cancel with
// an empty object, so a minimal "cancelling" batch is returned; clients retrieve the batch for the
// full state.
func (t *openAIToGCPVertexAIBatchCancelTranslator) ResponseBody(_ map[string]string, _ io.Reader, _ bool, _ any) (
	newHeaders []internalapi.Header, mutatedBody []byte, tokenUsage metrics.TokenUsage,
	responseModel internalapi.ResponseModel, err error,
) {
	batch := openai.Batch{
		ID:               t.nativeID,
		Object:           "batch",
		Endpoint:         gcpBatchEndpointForModel(t.model),
		Model:            t.model,
		CompletionWindow: string(openai.BatchNewParamsCompletionWindow24h),
		Status:           openai.BatchStatusCancelling,
	}
	now := gcpBatchNow().Unix()
	batch.CancellingAt = &now
	mutatedBody, err = json.Marshal(batch)
	if err != nil {
		return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("failed to encode batch: %w", err)
	}
	return jsonBodyHeaders(mutatedBody), mutatedBody, metrics.TokenUsage{}, t.model, nil
}

// RequestBody implements [Translator.RequestBody]. "limit" becomes pageSize and the native
// "after" cursor (a Vertex AI page token) becomes pageToken.
func (t *openAIToGCPVertexAIBatchListTranslator) RequestBody(_ []byte, req *BatchesRequest, _ bool) (
	newHeaders []internalapi.Header, mutatedBody []byte, err error,
) {
	t.model = req.Model
	if req.ModelNameOverride != "" {
		t.model = req.ModelNameOverride
	}
	in, err := url.ParseQuery(strings.TrimPrefix(req.Query, "?"))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: invalid query: %w", internalapi.ErrMalformedRequest, err)
	}
	out := url.Values{}
	if limit := in.Get("limit"); limit != "" {
		out.Set("pageSize", limit)
	}
	if after := in.Get("after"); after != "" {
		if isGCPBatchJobID(after) {
			return nil, nil, fmt.Errorf("%w: for GCP Vertex AI batches, pass the list response's last_id as 'after'",
				internalapi.ErrInvalidRequestBody)
		}
		out.Set("pageToken", after)
	}
	path := gcpBatchPredictionJobsPath
	if encoded := out.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return []internalapi.Header{{pathHeaderName, path}}, nil, nil
}

// ResponseBody implements [Translator.ResponseBody]. Jobs for other models are dropped, so a page
// may hold fewer than "limit" batches while has_more is still true.
func (t *openAIToGCPVertexAIBatchListTranslator) ResponseBody(_ map[string]string, body io.Reader, _ bool, _ any) (
	newHeaders []internalapi.Header, mutatedBody []byte, tokenUsage metrics.TokenUsage,
	responseModel internalapi.ResponseModel, err error,
) {
	var resp gcp.ListBatchPredictionJobsResponse
	if err = json.NewDecoder(body).Decode(&resp); err != nil {
		return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("failed to decode Vertex AI batch prediction job list: %w", err)
	}
	list := openai.BatchListResponse{Object: "list", Data: []openai.Batch{}, HasMore: resp.NextPageToken != ""}
	wantModel := gcpModelResourceName(t.model)
	for i := range resp.BatchPredictionJobs {
		job := &resp.BatchPredictionJobs[i]
		if t.model != "" && job.Model != wantModel {
			continue
		}
		batch, convErr := gcpBatchPredictionJobToOpenAI(job)
		if convErr != nil {
			return nil, nil, metrics.TokenUsage{}, "", convErr
		}
		list.Data = append(list.Data, *batch)
	}
	if len(list.Data) > 0 {
		list.FirstID = list.Data[0].ID
		list.LastID = list.Data[len(list.Data)-1].ID
	}
	if resp.NextPageToken != "" {
		// The cursor for the next page; encrypted into a gateway batch id by the processor.
		list.LastID = resp.NextPageToken
	}
	mutatedBody, err = json.Marshal(list)
	if err != nil {
		return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("failed to encode batch list: %w", err)
	}
	return jsonBodyHeaders(mutatedBody), mutatedBody, metrics.TokenUsage{}, "", nil
}

// gcpBatchPredictionJobToOpenAI converts a Vertex AI batch prediction job to an OpenAI batch.
// The returned id is the backend-native job id; the processor encodes it into a gateway id.
func gcpBatchPredictionJobToOpenAI(job *gcp.BatchPredictionJob) (*openai.Batch, error) {
	nativeID := job.Name[strings.LastIndex(job.Name, "/")+1:]
	if nativeID == "" {
		return nil, fmt.Errorf("missing name in Vertex AI batch prediction job")
	}
	model := strings.TrimPrefix(job.Model, gcpModelResourcePrefix)
	batch := &openai.Batch{
		ID:               nativeID,
		Object:           "batch",
		Endpoint:         gcpBatchEndpointForModel(model),
		Model:            model,
		CompletionWindow: string(openai.BatchNewParamsCompletionWindow24h),
		Status:           gcpJobStateToBatchStatus(job.State),
		Metadata:         job.Labels,
	}
	if in := job.InputConfig; in != nil && in.GCSSource != nil && len(in.GCSSource.URIs) > 0 {
		batch.InputFileID = in.GCSSource.URIs[0]
	}
	if out := job.OutputInfo; out != nil && out.GCSOutputDirectory != "" {
		dir := out.GCSOutputDirectory
		batch.OutputFileID = &dir
	}
	if job.Error != nil && (job.Error.Message != "" || job.Error.Code != nil) {
		e := openai.BatchError{Message: job.Error.Message}
		if job.Error.Code != nil {
			e.Code = strconv.Itoa(int(*job.Error.Code))
		}
		batch.Errors = &openai.BatchErrors{Object: "list", Data: []openai.BatchError{e}}
	}
	if s := job.CompletionStats; s != nil {
		total := s.SuccessfulCount + s.FailedCount
		if s.IncompleteCount > 0 {
			total += s.IncompleteCount
		}
		batch.RequestCounts = &openai.BatchRequestCounts{Total: total, Completed: s.SuccessfulCount, Failed: s.FailedCount}
	}

	var err error
	if batch.CreatedAt, err = parseGCPTime(job.CreateTime); err != nil {
		return nil, err
	}
	start, err := parseGCPTime(job.StartTime)
	if err != nil {
		return nil, err
	}
	if start != 0 {
		batch.InProgressAt = &start
	}
	end, err := parseGCPTime(job.EndTime)
	if err != nil {
		return nil, err
	}
	if end != 0 {
		switch batch.Status {
		case openai.BatchStatusCompleted:
			batch.CompletedAt = &end
		case openai.BatchStatusFailed:
			batch.FailedAt = &end
		case openai.BatchStatusCancelled:
			batch.CancelledAt = &end
		case openai.BatchStatusExpired:
			batch.ExpiredAt = &end
		}
	}
	return batch, nil
}

// gcpJobStateToBatchStatus maps a Vertex AI job state to an OpenAI batch status.
func gcpJobStateToBatchStatus(state gcp.JobState) openai.BatchStatus {
	switch state {
	case gcp.JobStateRunning, gcp.JobStateUpdating, gcp.JobStatePaused:
		return openai.BatchStatusInProgress
	case gcp.JobStateSucceeded, gcp.JobStatePartiallySucceeded:
		return openai.BatchStatusCompleted
	case gcp.JobStateFailed:
		return openai.BatchStatusFailed
	case gcp.JobStateCancelling:
		return openai.BatchStatusCancelling
	case gcp.JobStateCancelled:
		return openai.BatchStatusCancelled
	case gcp.JobStateExpired:
		return openai.BatchStatusExpired
	default: // queued, pending, unspecified
		return openai.BatchStatusValidating
	}
}

// gcpBatchEndpointForModel infers the OpenAI batch endpoint from a Vertex AI model name, since
// the job does not record it.
func gcpBatchEndpointForModel(model string) openai.BatchNewParamsEndpoint {
	if strings.Contains(model, "embedding") {
		return openai.BatchNewParamsEndpointV1Embeddings
	}
	return openai.BatchNewParamsEndpointV1ChatCompletions
}

// gcpModelResourceName returns the Vertex AI publisher model resource name for a model. Models
// that are already resource names are returned unchanged.
func gcpModelResourceName(model string) string {
	if model == "" || strings.Contains(model, "/") {
		return model
	}
	return gcpModelResourcePrefix + model
}

// defaultGCSOutputPrefix returns "<input directory>/output/" for a gs:// input URI.
func defaultGCSOutputPrefix(inputURI string) string {
	rest := strings.TrimPrefix(inputURI, gcsURIScheme)
	if i := strings.LastIndex(rest, "/"); i != -1 {
		return gcsURIScheme + rest[:i] + "/output/"
	}
	return gcsURIScheme + rest + "/output/"
}

// isGCPBatchJobID reports whether s looks like a numeric Vertex AI job id rather than a page token.
func isGCPBatchJobID(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// parseGCPTime parses an RFC3339 Vertex AI timestamp into Unix seconds; "" yields 0.
func parseGCPTime(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, fmt.Errorf("invalid timestamp %q in Vertex AI batch prediction job: %w", s, err)
	}
	return t.Unix(), nil
}

// sanitizeGCPLabels converts arbitrary key/value pairs to valid Vertex AI labels: lowercase
// letters, digits, '_' and '-', at most 63 characters, keys starting with a letter, and at most
// 64 labels. Keys that sanitize to nothing are dropped.
func sanitizeGCPLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		key := sanitizeGCPLabelPart(k)
		if key == "" {
			continue
		}
		if r := rune(key[0]); !unicode.IsLetter(r) {
			key = "m_" + key
			if len(key) > gcpMaxLabelLength {
				key = key[:gcpMaxLabelLength]
			}
		}
		out[key] = sanitizeGCPLabelPart(v)
		if len(out) == gcpMaxLabels {
			break
		}
	}
	return out
}

func sanitizeGCPLabelPart(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if b.Len() == gcpMaxLabelLength {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// jsonBodyHeaders returns the content headers for a translated JSON body.
func jsonBodyHeaders(body []byte) []internalapi.Header {
	return []internalapi.Header{
		{contentTypeHeaderName, jsonContentType},
		{contentLengthHeaderName, strconv.Itoa(len(body))},
	}
}
