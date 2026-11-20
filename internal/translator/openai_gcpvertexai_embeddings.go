// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"google.golang.org/genai"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	tracing "github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

const (
	gcpMethodPredict            = "predict"
	gcpMethodEmbedContent       = "embedContent"
	gcpMethodBatchEmbedContents = "batchEmbedContents"
)

// NewEmbeddingOpenAIToGCPVertexAITranslator implements [Factory] for OpenAI to GCP VertexAI translation
// for embeddings.
func NewEmbeddingOpenAIToGCPVertexAITranslator(requestModel internalapi.RequestModel, modelNameOverride internalapi.ModelNameOverride) OpenAIEmbeddingTranslator {
	return &openAIToGCPVertexAITranslatorV1Embedding{
		requestModel:      requestModel,
		modelNameOverride: modelNameOverride,
	}
}

// openAIToGCPVertexAITranslatorV1Embedding translates OpenAI Embeddings API to GCP Vertex AI Gemini Embeddings API.
// It auto-detects the endpoint based on model name:
//   - Older models (text-embedding-004, gemini-embedding-001): predict endpoint
//   - Newer models (gemini-embedding-2-*): embedContent endpoint for a single input, batchEmbedContents
//     endpoint for multiple string inputs
//
// https://cloud.google.com/vertex-ai/generative-ai/docs/model-reference/text-embeddings-api
type openAIToGCPVertexAITranslatorV1Embedding struct {
	requestModel      internalapi.RequestModel
	modelNameOverride internalapi.ModelNameOverride
	useEmbedContent   bool
	// useBatchEmbedContents is set when the request was sent to batchEmbedContents, whose response
	// carries an embeddings array instead of embedContent's single embedding.
	useBatchEmbedContents bool
}

// createInstancesFromEmbeddingInputItem converts an EmbeddingInputItem to GCP Instance(s).
// This handles the mapping of OpenAI's extended embedding input format to GCP's instance format,
// including task_type and title metadata for optimized embedding generation.
// When content is an array of strings, each string becomes a separate instance with the same task_type.
func createInstancesFromEmbeddingInputItem(item openai.EmbeddingInputItem, instances []*gcp.Instance) []*gcp.Instance {
	switch v := item.Content.Value.(type) {
	case string:
		instance := &gcp.Instance{Content: v}
		if item.TaskType != "" {
			instance.TaskType = item.TaskType
		}
		// Title is only valid with task_type=RETRIEVAL_DOCUMENT.
		// See: https://cloud.google.com/vertex-ai/generative-ai/docs/embeddings/task-types
		if item.TaskType == openai.EmbeddingTaskTypeRetrievalDocument && item.Title != "" {
			instance.Title = item.Title
		}
		instances = append(instances, instance)
	case []string:
		// Multiple strings with the same task_type - each becomes a separate instance
		for _, text := range v {
			instance := &gcp.Instance{Content: text}
			if item.TaskType != "" {
				instance.TaskType = item.TaskType
			}
			// Title is only valid with task_type=RETRIEVAL_DOCUMENT.
			if item.TaskType == openai.EmbeddingTaskTypeRetrievalDocument && item.Title != "" {
				instance.Title = item.Title
			}
			instances = append(instances, instance)
		}
	}
	return instances
}

// setInstances converts OpenAI embedding input to GCP instances.
// It handles multiple input formats: string, []string, EmbeddingInputItem, []EmbeddingInputItem.
// Each input element is converted to a separate GCP Instance for batch embedding generation.
func setInstances(input openai.EmbeddingRequestInput, instances []*gcp.Instance) ([]*gcp.Instance, error) {
	switch v := input.Value.(type) {
	case string:
		instances = append(instances, &gcp.Instance{Content: v})
		return instances, nil
	case []string:
		// Array of strings: create a separate instance for each string.
		for _, text := range v {
			instances = append(instances, &gcp.Instance{Content: text})
		}
		return instances, nil
	case openai.EmbeddingInputItem:
		// Single EmbeddingInputItem with enhanced metadata.
		// Content can be string or []string.
		instances = createInstancesFromEmbeddingInputItem(v, instances)
		return instances, nil
	case []openai.EmbeddingInputItem:
		// Array of EmbeddingInputItem objects with metadata support.
		for _, item := range v {
			instances = createInstancesFromEmbeddingInputItem(item, instances)
		}
		return instances, nil
	default:
		return nil, fmt.Errorf("%w: unsupported input type for embedding: %T (supported: string, []string, EmbeddingInputItem, []EmbeddingInputItem)", internalapi.ErrInvalidRequestBody, v)
	}
}

// openAIEmbeddingToGeminiMessage converts an OpenAI EmbeddingCompletionRequest to a GCP PredictRequest.
func openAIEmbeddingToGeminiMessage(openAIReq *openai.EmbeddingRequest) (*gcp.PredictRequest, error) {
	if openAIReq.OfCompletion == nil {
		return nil, fmt.Errorf("%w: model %s does not support multimodal embedding via messages", internalapi.ErrInvalidRequestBody, openAIReq.Model)
	}
	// Convert OpenAI EmbeddingRequest's input to Gemini instances.
	var instances []*gcp.Instance
	instances, err := setInstances(openAIReq.OfCompletion.Input, instances)
	if err != nil {
		return nil, err
	}

	// Create the embedding prediction parameters.
	parameters := &gcp.Parameters{}

	// Set output dimensionality if specified.
	if openAIReq.Dimensions != nil && *openAIReq.Dimensions > 0 {
		parameters.OutputDimensionality = *openAIReq.Dimensions
	}

	// Apply vendor-specific fields if present.
	if openAIReq.GCPVertexAIEmbeddingVendorFields != nil {
		// Set auto truncate if specified.
		if openAIReq.AutoTruncate != nil {
			parameters.AutoTruncate = *openAIReq.AutoTruncate
		}

		// Apply global task type to all instances if specified.
		// This overrides any task_type set on individual input items.
		if openAIReq.TaskType != "" {
			for _, instance := range instances {
				instance.TaskType = openAIReq.TaskType
			}
		}
		// Apply global title to all instances if specified.
		if openAIReq.Title != "" {
			for _, instance := range instances {
				instance.Title = openAIReq.Title
			}
		}
	}

	// Build the request using gcp.PredictRequest.
	gcr := &gcp.PredictRequest{
		Instances:  instances,
		Parameters: *parameters,
	}

	return gcr, nil
}

// isEmbedContentModel returns true if the model should use the embedContent endpoint
// instead of the predict endpoint.
// Reference: https://github.com/googleapis/go-genai/blob/v1.54.0/transformer.go#L565
// Check and update this function when new Gemini embedding model versions are released.
func isEmbedContentModel(model string) bool {
	return strings.Contains(model, "gemini") && model != "gemini-embedding-001"
}

// collectInputTexts extracts all input text strings from an OpenAI EmbeddingRequestInput.
// Only plain string inputs are supported for the embedContent endpoint.
// EmbeddingInputItem (with per-item task_type/title) is rejected — use global vendor fields instead.
func collectInputTexts(input openai.EmbeddingRequestInput) ([]string, error) {
	switch v := input.Value.(type) {
	case string:
		return []string{v}, nil
	case []string:
		return v, nil
	case openai.EmbeddingInputItem:
		return nil, fmt.Errorf("%w: object input with per-item task_type/title is not supported for this model; use plain string input and set task_type at the request level", internalapi.ErrInvalidRequestBody)
	case []openai.EmbeddingInputItem:
		return nil, fmt.Errorf("%w: object input with per-item task_type/title is not supported for this model; use plain string input and set task_type at the request level", internalapi.ErrInvalidRequestBody)
	default:
		return nil, fmt.Errorf("%w: unsupported input type for embedding: %T", internalapi.ErrInvalidRequestBody, v)
	}
}

// collectPartsFromMessages extracts genai.Part objects from chat messages for embedding.
// Only user messages are processed; system/assistant/tool/developer messages are skipped.
// Supported content types: text, images (URL or data URI), input_audio, and file data URIs (e.g. PDF or video).
// Whether non-text parts are accepted depends on the target embedding model (e.g. gemini-embedding-2).
func collectPartsFromMessages(messages []openai.ChatCompletionMessageParamUnion, requestModel internalapi.RequestModel) ([]*genai.Part, error) {
	var parts []*genai.Part
	for _, msg := range messages {
		if msg.OfUser == nil {
			continue
		}
		msgParts, err := userMsgToGeminiParts(*msg.OfUser, requestModel)
		if err != nil {
			return nil, err
		}
		parts = append(parts, msgParts...)
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("%w: no user messages found in embedding request", internalapi.ErrInvalidRequestBody)
	}
	return parts, nil
}

// openAIEmbeddingToEmbedContentRequests converts an OpenAI EmbeddingRequest to one GCP EmbedContentRequest
// per embedding the client expects back.
//   - Chat messages: all multimodal parts from user messages go into one Content, producing one embedding.
//   - String input: each string becomes its own Content, producing one embedding per string. Packing the
//     strings as parts of a single Content would merge them into one embedding instead.
func openAIEmbeddingToEmbedContentRequests(openAIReq *openai.EmbeddingRequest, requestModel internalapi.RequestModel) ([]gcp.EmbedContentRequest, error) {
	config := embedContentConfigFromOpenAI(openAIReq)

	switch {
	case openAIReq.OfChat != nil:
		parts, err := collectPartsFromMessages(openAIReq.OfChat.Messages, requestModel)
		if err != nil {
			return nil, err
		}
		return []gcp.EmbedContentRequest{{Content: genai.Content{Parts: parts}, Config: config}}, nil
	case openAIReq.OfCompletion != nil:
		texts, err := collectInputTexts(openAIReq.OfCompletion.Input)
		if err != nil {
			return nil, err
		}
		if len(texts) == 0 {
			return nil, fmt.Errorf("%w: embedding input must not be empty", internalapi.ErrInvalidRequestBody)
		}
		reqs := make([]gcp.EmbedContentRequest, len(texts))
		for i, text := range texts {
			// Every entry shares the same config: batchEmbedContents rejects entries whose
			// outputDimensionality or autoTruncate differ from the first entry.
			reqs[i] = gcp.EmbedContentRequest{
				Content: genai.Content{Parts: []*genai.Part{genai.NewPartFromText(text)}},
				Config:  config,
			}
		}
		return reqs, nil
	default:
		return nil, fmt.Errorf("%w: embedding request must have either input or messages", internalapi.ErrInvalidRequestBody)
	}
}

// embedContentConfigFromOpenAI builds the embedContentConfig from request-level OpenAI fields.
// It returns nil when no field is set so that embedContentConfig is omitted from the request.
// Config fields are sent via "embedContentConfig" (not deprecated top-level fields).
// https://github.com/googleapis/go-genai/blob/v1.54.0/models.go#L727
func embedContentConfigFromOpenAI(openAIReq *openai.EmbeddingRequest) *gcp.EmbedContentConfig {
	var config gcp.EmbedContentConfig
	hasConfig := false

	if openAIReq.Dimensions != nil && *openAIReq.Dimensions > 0 {
		config.OutputDimensionality = *openAIReq.Dimensions
		hasConfig = true
	}

	if openAIReq.GCPVertexAIEmbeddingVendorFields != nil {
		if openAIReq.AutoTruncate != nil {
			config.AutoTruncate = openAIReq.AutoTruncate
			hasConfig = true
		}
		// NOTE: gemini-embedding-2 silently ignores taskType — task must be included as a prompt
		// instruction instead. We still send it in case future embedContent models support it.
		// https://docs.cloud.google.com/vertex-ai/generative-ai/docs/embeddings/get-multimodal-embeddings
		if openAIReq.TaskType != "" {
			config.TaskType = openAIReq.TaskType
			hasConfig = true
		}
		if openAIReq.Title != "" {
			config.Title = openAIReq.Title
			hasConfig = true
		}
	}

	if !hasConfig {
		return nil
	}
	return &config
}

// RequestBody implements [OpenAIEmbeddingTranslator.RequestBody] for GCP Gemini.
// This method translates an OpenAI Embedding request to a GCP Gemini Embeddings API request.
func (o *openAIToGCPVertexAITranslatorV1Embedding) RequestBody(_ []byte, req *openai.EmbeddingRequest, _ bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	o.requestModel = req.Model
	if o.modelNameOverride != "" {
		// Use modelName override if set.
		o.requestModel = o.modelNameOverride
	}

	var path string

	if isEmbedContentModel(o.requestModel) {
		var gcpReqs []gcp.EmbedContentRequest
		gcpReqs, err = openAIEmbeddingToEmbedContentRequests(req, o.requestModel)
		if err != nil {
			return nil, nil, fmt.Errorf("error converting EmbeddingRequest: %w", err)
		}
		if len(gcpReqs) == 1 {
			o.useEmbedContent, o.useBatchEmbedContents = true, false
			path = buildGCPModelPathSuffix(gcpModelPublisherGoogle, o.requestModel, gcpMethodEmbedContent)
			newBody, err = json.Marshal(gcpReqs[0])
		} else {
			o.useEmbedContent, o.useBatchEmbedContents = false, true
			path = buildGCPModelPathSuffix(gcpModelPublisherGoogle, o.requestModel, gcpMethodBatchEmbedContents)
			newBody, err = json.Marshal(gcp.BatchEmbedContentsRequest{Requests: gcpReqs})
		}
	} else {
		o.useEmbedContent, o.useBatchEmbedContents = false, false

		if req.OfChat != nil {
			return nil, nil, fmt.Errorf("%w: model %s does not support multimodal embedding via messages", internalapi.ErrInvalidRequestBody, o.requestModel)
		}

		path = buildGCPModelPathSuffix(gcpModelPublisherGoogle, o.requestModel, gcpMethodPredict)

		var gcpReq *gcp.PredictRequest
		gcpReq, err = openAIEmbeddingToGeminiMessage(req)
		if err != nil {
			return nil, nil, fmt.Errorf("error converting EmbeddingRequest: %w", err)
		}
		newBody, err = json.Marshal(gcpReq)
	}

	if err != nil {
		return nil, nil, fmt.Errorf("error marshaling Gemini request: %w", err)
	}
	newHeaders = []internalapi.Header{
		{pathHeaderName, path},
		{contentLengthHeaderName, strconv.Itoa(len(newBody))},
	}
	return
}

// ResponseHeaders implements [OpenAIEmbeddingTranslator.ResponseHeaders].
func (o *openAIToGCPVertexAITranslatorV1Embedding) ResponseHeaders(_ map[string]string) (newHeaders []internalapi.Header, err error) {
	return nil, nil
}

// ResponseBody implements [OpenAIEmbeddingTranslator.ResponseBody] for GCP Gemini.
// This method translates a GCP Gemini Embeddings API response to the OpenAI Embeddings format.
// GCP Vertex AI uses deterministic model mapping without virtualization, where the requested model
// is exactly what gets executed. The response does not contain a model field, so we return
// the request model that was originally sent.
func (o *openAIToGCPVertexAITranslatorV1Embedding) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracing.EmbeddingsSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	// Read the Gemini embedding response.
	respBody, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("failed to read gemini embedding response body: %w", err)
	}

	var openaiResp openai.EmbeddingResponse
	var promptTokens int

	switch {
	case o.useBatchEmbedContents:
		openaiResp, promptTokens, err = o.parseBatchEmbedContentsResponse(respBody)
	case o.useEmbedContent:
		openaiResp, promptTokens, err = o.parseEmbedContentResponse(respBody)
	default:
		openaiResp, promptTokens, err = o.parsePredictResponse(respBody)
	}
	if err != nil {
		return nil, nil, tokenUsage, "", err
	}

	// Set token usage from accumulated values.
	openaiResp.Usage.PromptTokens = promptTokens
	openaiResp.Usage.TotalTokens = promptTokens

	// Marshal the OpenAI response.
	newBody, err = json.Marshal(openaiResp)
	if err != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("failed to marshal OpenAI response: %w", err)
	}

	// Update token usage metrics.
	tokenUsage.SetInputTokens(uint32(promptTokens)) //nolint:gosec
	tokenUsage.SetTotalTokens(uint32(promptTokens)) //nolint:gosec

	// Record the response in the span for tracing.
	if span != nil {
		span.RecordResponse(&openaiResp)
	}

	newHeaders = []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(newBody))}}
	responseModel = openaiResp.Model
	return
}

// parsePredictResponse parses a GCP PredictResponse and converts it to the OpenAI format.
func (o *openAIToGCPVertexAITranslatorV1Embedding) parsePredictResponse(respBody []byte) (openai.EmbeddingResponse, int, error) {
	var gcpResp gcp.PredictResponse
	if err := json.Unmarshal(respBody, &gcpResp); err != nil {
		return openai.EmbeddingResponse{}, 0, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	openaiResp := openai.EmbeddingResponse{
		Object: "list",
		Model:  o.requestModel,
	}

	var promptTokens int
	if len(gcpResp.Predictions) > 0 {
		openaiResp.Data = make([]openai.Embedding, len(gcpResp.Predictions))
		for i, prediction := range gcpResp.Predictions {
			if prediction != nil {
				float64Values := make([]float64, len(prediction.Embeddings.Values))
				for j, v := range prediction.Embeddings.Values {
					float64Values[j] = float64(v)
				}
				openaiResp.Data[i] = openai.Embedding{
					Object:    "embedding",
					Index:     i,
					Embedding: openai.EmbeddingUnion{Value: float64Values},
				}
				if prediction.Embeddings.Statistics != nil {
					promptTokens += prediction.Embeddings.Statistics.TokenCount
					openaiResp.Data[i].Truncated = prediction.Embeddings.Statistics.Truncated
				}
			}
		}
	} else {
		openaiResp.Data = []openai.Embedding{}
	}

	return openaiResp, promptTokens, nil
}

// parseEmbedContentResponse parses a GCP EmbedContentResponse and converts it to the OpenAI format.
func (o *openAIToGCPVertexAITranslatorV1Embedding) parseEmbedContentResponse(respBody []byte) (openai.EmbeddingResponse, int, error) {
	var gcpResp gcp.EmbedContentResponse
	if err := json.Unmarshal(respBody, &gcpResp); err != nil {
		return openai.EmbeddingResponse{}, 0, fmt.Errorf("failed to unmarshal embedContent response: %w", err)
	}

	openaiResp := openai.EmbeddingResponse{
		Object: "list",
		Model:  o.requestModel,
	}

	var promptTokens int
	if gcpResp.Embedding != nil {
		float64Values := make([]float64, len(gcpResp.Embedding.Values))
		for j, v := range gcpResp.Embedding.Values {
			float64Values[j] = float64(v)
		}
		openaiResp.Data = []openai.Embedding{{
			Object:    "embedding",
			Index:     0,
			Embedding: openai.EmbeddingUnion{Value: float64Values},
			Truncated: gcpResp.Truncated,
		}}
		if gcpResp.UsageMetadata != nil {
			promptTokens = gcpResp.UsageMetadata.PromptTokenCount
		}
	} else {
		openaiResp.Data = []openai.Embedding{}
	}

	return openaiResp, promptTokens, nil
}

// parseBatchEmbedContentsResponse parses a GCP BatchEmbedContentsResponse and converts it to the OpenAI format.
// Embeddings are returned in request order, so the array position is the OpenAI index. The batch response
// has no per-embedding truncation flag, so Truncated is left false.
func (o *openAIToGCPVertexAITranslatorV1Embedding) parseBatchEmbedContentsResponse(respBody []byte) (openai.EmbeddingResponse, int, error) {
	var gcpResp gcp.BatchEmbedContentsResponse
	if err := json.Unmarshal(respBody, &gcpResp); err != nil {
		return openai.EmbeddingResponse{}, 0, fmt.Errorf("failed to unmarshal batchEmbedContents response: %w", err)
	}

	openaiResp := openai.EmbeddingResponse{
		Object: "list",
		Model:  o.requestModel,
		Data:   make([]openai.Embedding, len(gcpResp.Embeddings)),
	}
	for i, emb := range gcpResp.Embeddings {
		float64Values := []float64{}
		if emb != nil {
			float64Values = make([]float64, len(emb.Values))
			for j, v := range emb.Values {
				float64Values[j] = float64(v)
			}
		}
		openaiResp.Data[i] = openai.Embedding{
			Object:    "embedding",
			Index:     i,
			Embedding: openai.EmbeddingUnion{Value: float64Values},
		}
	}

	var promptTokens int
	if gcpResp.UsageMetadata != nil {
		promptTokens = gcpResp.UsageMetadata.PromptTokenCount
	}
	return openaiResp, promptTokens, nil
}

// ResponseError implements [OpenAIEmbeddingTranslator.ResponseError].
// Translate GCP Vertex AI exceptions to OpenAI error type.
// GCP error responses typically contain JSON with error details or plain text error messages.
func (o *openAIToGCPVertexAITranslatorV1Embedding) ResponseError(respHeaders map[string]string, body io.Reader) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	return convertGCPVertexAIErrorToOpenAI(respHeaders, body)
}
