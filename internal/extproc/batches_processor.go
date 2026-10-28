// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/idcodec"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/translator"
)

// batchesPathMarker is the canonical Batch API path segment. The registered route prefix may be
// preceded by a configurable root/OpenAI prefix, so the operation is derived from the suffix
// that follows this marker rather than from the full path.
const batchesPathMarker = "/v1/batches"

// batchFileIDFields are the batch object fields that carry file ids. They are rewritten between
// gateway file ids and backend-native file ids.
var batchFileIDFields = []string{"input_file_id", "output_file_id", "error_file_id"}

// batchesOperation identifies which Batch API endpoint a request targets.
type batchesOperation int

const (
	batchesOpCreate   batchesOperation = iota // POST /v1/batches
	batchesOpList                             // GET  /v1/batches
	batchesOpRetrieve                         // GET  /v1/batches/{id}
	batchesOpCancel                           // POST /v1/batches/{id}/cancel
)

// NewBatchesProcessorFactory returns a [ProcessorFactory] for the OpenAI Batch API endpoints
// ("/v1/batches", "/v1/batches/{id}", "/v1/batches/{id}/cancel"), using the given backend id codec.
//
// Requests are routed by model, and each route must resolve to exactly one backend:
//
//   - Create: routed by the backend and model recorded in the gateway "input_file_id", which is
//     rewritten to the backend-native file id before forwarding.
//   - Retrieve/cancel: the gateway batch id in the path is decoded and the path rewritten to the
//     backend-native id. The decoded backend must be the one serving the matched route.
//   - List: routed by "?model=" or the model recorded in a gateway "after" batch id.
//   - Responses: "id" becomes a gateway batch id and input/output/error file ids become gateway
//     file ids for the same backend and model, so the output can be fetched via the Files API.
func NewBatchesProcessorFactory(codec idcodec.Codec) ProcessorFactory {
	return func(config *filterapi.RuntimeConfig, requestHeaders map[string]string, logger *slog.Logger, isUpstreamFilter bool, _ bool) (Processor, error) {
		return &batchesProcessor{
			codec:            codec,
			config:           config,
			requestHeaders:   requestHeaders,
			logger:           logger,
			isUpstreamFilter: isUpstreamFilter,
		}, nil
	}
}

// batchesProcessor implements [Processor] for the Batch API at both the router and upstream
// filter levels, mirroring [filesProcessor]: the router instance holds the per-request state and
// rewrites ids, while the upstream instance only captures the selected backend via SetBackend.
type batchesProcessor struct {
	codec            idcodec.Codec
	config           *filterapi.RuntimeConfig
	requestHeaders   map[string]string
	logger           *slog.Logger
	isUpstreamFilter bool

	op batchesOperation
	// backend is the LB-selected backend used to encode response ids; set only by SetBackend.
	backend *backendKey
	// decodedBackend is the backend decoded from a request id (input_file_id, path batch id, or a
	// list "after" id). SetBackend verifies the route-selected backend matches it.
	decodedBackend *backendKey
	// routingModel is the model used to route the request, recorded in minted response ids.
	routingModel string

	translator      translator.BatchesTranslator
	responseHeaders map[string]string

	// batchesRequestMetadata is the upstream request (without body) for translator.RequestBody.
	batchesRequestMetadata translator.BatchesRequest
	// requestBody is the native-id-substituted create body for translator.RequestBody.
	requestBody []byte

	// routeProcessor is the router processor, set by SetBackend on the upstream filter.
	routeProcessor *batchesProcessor
}

var _ Processor = (*batchesProcessor)(nil)

// ProcessRequestHeaders implements [Processor.ProcessRequestHeaders].
func (p *batchesProcessor) ProcessRequestHeaders(_ context.Context, _ *corev3.HeaderMap) (*extprocv3.ProcessingResponse, error) {
	if p.isUpstreamFilter {
		return p.translateUpstreamRequest()
	}

	method := p.requestHeaders[":method"]
	path := p.requestHeaders[":path"]
	op, id, err := classifyBatchesRequest(method, path)
	if err != nil {
		p.logger.Error("unsupported batches request", slog.String("method", method), slog.String("path", path))
		return createUserFacingErrorResponse(http.StatusNotFound, "NotFoundError", "unsupported Batch API request"), nil
	}
	p.op = op
	p.batchesRequestMetadata = translator.BatchesRequest{Path: path, Method: method}

	switch op {
	case batchesOpCreate:
		// Routing depends on input_file_id in the JSON body; defer to ProcessRequestBody.
		return requestHeadersResponse(p.newRequestHeaderMutation(), false), nil
	case batchesOpList:
		return p.handleListRequestHeaders()
	default: // retrieve, cancel
		return p.handleIDBearingRequestHeaders(op, id)
	}
}

// translateUpstreamRequest runs translator.RequestBody at the upstream filter, after SetBackend,
// and applies the returned headers and body. It mirrors filesProcessor.translateUpstreamRequest.
// The create translator owns provider-specific endpoint support: an error wrapping
// [translator.ErrUnsupportedBatchEndpoint] becomes a 400.
func (p *batchesProcessor) translateUpstreamRequest() (*extprocv3.ProcessingResponse, error) {
	rp := p.routeProcessor
	if rp == nil || rp.translator == nil {
		// No backend resolved.
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestHeaders{}}, nil
	}
	newHeaders, newBody, err := rp.translator.RequestBody(rp.requestBody, &rp.batchesRequestMetadata, false)
	if err != nil {
		if errors.Is(err, translator.ErrUnsupportedBatchEndpoint) {
			p.logger.Info("rejecting batch create with endpoint unsupported by backend",
				slog.String("endpoint", string(rp.batchesRequestMetadata.Endpoint)), slog.String("error", err.Error()))
			return createUserFacingErrorResponse(http.StatusBadRequest, "invalid_request_error", err.Error()), nil
		}
		if userFacingErr := internalapi.GetUserFacingError(err); userFacingErr != nil {
			p.logger.Info("returning user-facing error for invalid batches request", slog.String("error", err.Error()))
			return createUserFacingErrorResponse(http.StatusUnprocessableEntity, "UnprocessableEntity", userFacingErr.Error()), nil
		}
		return nil, fmt.Errorf("failed to transform batches request: %w", err)
	}
	headerMutation, bodyMutation := mutationsFromTranslationResult(newHeaders, newBody)
	if bodyMutation == nil {
		return &extprocv3.ProcessingResponse{
			Response: &extprocv3.ProcessingResponse_RequestHeaders{
				RequestHeaders: &extprocv3.HeadersResponse{
					Response: &extprocv3.CommonResponse{
						HeaderMutation: headerMutation,
						Status:         extprocv3.CommonResponse_CONTINUE,
					},
				},
			},
		}, nil
	}
	setHeader(headerMutation, "content-length", strconv.Itoa(len(newBody)))
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_RequestHeaders{
			RequestHeaders: &extprocv3.HeadersResponse{
				Response: &extprocv3.CommonResponse{
					HeaderMutation: headerMutation,
					BodyMutation:   bodyMutation,
					Status:         extprocv3.CommonResponse_CONTINUE_AND_REPLACE,
				},
			},
		},
		DynamicMetadata: buildContentLengthDynamicMetadataOnRequest(len(newBody)),
	}, nil
}

// handleListRequestHeaders sets up routing for GET /v1/batches. It is routed by "?model=", falling
// back to the model recorded in a gateway "after" batch id, which is rewritten to the native id.
// The routing-only "model" parameter is never forwarded upstream.
func (p *batchesProcessor) handleListRequestHeaders() (*extprocv3.ProcessingResponse, error) {
	path := p.requestHeaders[":path"]
	headerMutation := p.newRequestHeaderMutation()

	model := queryParam(path, "model")
	upstreamPath := setQueryParam(path, "model", "")

	if after := queryParam(path, "after"); after != "" {
		decoded, err := p.codec.Decode(after)
		if err != nil || decoded.Kind != idcodec.KindBatch {
			p.logger.Error("rejecting batches list request with undecodable after id", slog.String("after", after))
			return createUserFacingErrorResponse(http.StatusBadRequest, "invalid_request_error", "invalid after cursor"), nil
		}
		p.decodedBackend = &backendKey{namespace: decoded.Namespace, name: decoded.Name}
		if model == "" {
			model = decoded.Model
		}
		upstreamPath = setQueryParam(upstreamPath, "after", decoded.NativeID)
	}

	if model == "" {
		p.logger.Error("list batches request is missing the required model query parameter", slog.String("path", path))
		return createUserFacingErrorResponse(http.StatusBadRequest, "invalid_request_error", "the 'model' query parameter is required"), nil
	}
	p.routeByModel(headerMutation, model)
	setHeader(headerMutation, ":path", upstreamPath)
	p.batchesRequestMetadata.Path = upstreamPath
	p.batchesRequestMetadata.Model = model
	_, p.batchesRequestMetadata.Query = splitQuery(upstreamPath)
	return requestHeadersResponse(headerMutation, true), nil
}

// handleIDBearingRequestHeaders decodes the gateway batch id in the path, routes by the model
// recorded in it, and rewrites the path to the backend-native id. A forged id or an id of another
// kind yields a 404.
func (p *batchesProcessor) handleIDBearingRequestHeaders(op batchesOperation, gatewayID string) (*extprocv3.ProcessingResponse, error) {
	decoded, err := p.codec.Decode(gatewayID)
	if err != nil || decoded.Kind != idcodec.KindBatch {
		p.logger.Error("rejecting batches request with undecodable id", slog.String("id", gatewayID))
		return createUserFacingErrorResponse(http.StatusNotFound, "NotFoundError", fmt.Sprintf("No such Batch object: %s", gatewayID)), nil
	}
	p.decodedBackend = &backendKey{namespace: decoded.Namespace, name: decoded.Name}
	p.routingModel = decoded.Model

	headerMutation := p.newRequestHeaderMutation()
	nativePath := p.rewrittenPath(op, decoded.NativeID)
	setHeader(headerMutation, ":path", nativePath)
	p.batchesRequestMetadata.NativeID = decoded.NativeID
	p.batchesRequestMetadata.Path = nativePath
	p.batchesRequestMetadata.Model = decoded.Model
	if decoded.Model != "" {
		p.routeByModel(headerMutation, decoded.Model)
	}
	return requestHeadersResponse(headerMutation, true), nil
}

// newRequestHeaderMutation returns a header mutation that preserves the original (client-facing)
// request path in originalPathHeader, so the upstream filter resolves this processor.
func (p *batchesProcessor) newRequestHeaderMutation() *extprocv3.HeaderMutation {
	headerMutation := &extprocv3.HeaderMutation{}
	setHeader(headerMutation, originalPathHeader, p.requestHeaders[":path"])
	return headerMutation
}

// routeByModel records model as the routing model and sets the model header used for route matching.
func (p *batchesProcessor) routeByModel(headerMutation *extprocv3.HeaderMutation, model string) {
	p.routingModel = model
	setHeader(headerMutation, internalapi.ModelNameHeaderKeyDefault, model)
	p.requestHeaders[internalapi.ModelNameHeaderKeyDefault] = model
}

// ProcessRequestBody implements [Processor.ProcessRequestBody]. Only create has a body to process:
// the gateway input_file_id is decoded to route the request and replaced by the native file id.
func (p *batchesProcessor) ProcessRequestBody(_ context.Context, rawBody *extprocv3.HttpBody) (*extprocv3.ProcessingResponse, error) {
	if p.isUpstreamFilter || p.op != batchesOpCreate {
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestBody{}}, nil
	}
	body := rawBody.GetBody()
	if !gjson.ValidBytes(body) {
		return createUserFacingErrorResponse(http.StatusBadRequest, "invalid_request_error", "request body must be a JSON object"), nil
	}

	// Endpoint support is provider-specific, so it is checked by the create translator in the
	// upstream filter once the backend is known (see translateUpstreamRequest).
	p.batchesRequestMetadata.Endpoint = openai.BatchNewParamsEndpoint(gjson.GetBytes(body, "endpoint").String())

	inputFileID := gjson.GetBytes(body, "input_file_id").String()
	if inputFileID == "" {
		return createUserFacingErrorResponse(http.StatusBadRequest, "invalid_request_error", "the 'input_file_id' field is required"), nil
	}
	decoded, err := p.codec.Decode(inputFileID)
	if err != nil || decoded.Kind != idcodec.KindFile {
		p.logger.Info("rejecting batch create with undecodable input_file_id", slog.String("input_file_id", inputFileID))
		return createUserFacingErrorResponse(http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("invalid input_file_id: %s", inputFileID)), nil
	}
	if decoded.Model == "" {
		return createUserFacingErrorResponse(http.StatusBadRequest, "invalid_request_error",
			"input_file_id carries no routing model"), nil
	}
	p.decodedBackend = &backendKey{namespace: decoded.Namespace, name: decoded.Name}

	newBody, err := sjson.SetBytes(body, "input_file_id", decoded.NativeID)
	if err != nil {
		p.logger.Error("failed to rewrite batch input_file_id", slog.String("error", err.Error()))
		return createUserFacingErrorResponse(http.StatusInternalServerError, "internal_error", "failed to rewrite input_file_id"), nil
	}
	p.requestBody = newBody

	headerMutation := p.newRequestHeaderMutation()
	p.routeByModel(headerMutation, decoded.Model)
	p.batchesRequestMetadata.Model = decoded.Model
	setHeader(headerMutation, "content-length", strconv.Itoa(len(newBody)))
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_RequestBody{RequestBody: &extprocv3.BodyResponse{
			Response: &extprocv3.CommonResponse{
				HeaderMutation:  headerMutation,
				BodyMutation:    &extprocv3.BodyMutation{Mutation: &extprocv3.BodyMutation_Body{Body: newBody}},
				ClearRouteCache: true,
			},
		}},
	}, nil
}

// ProcessResponseHeaders implements [Processor.ProcessResponseHeaders]. Captures the upstream
// response headers for the status check and translator calls.
func (p *batchesProcessor) ProcessResponseHeaders(_ context.Context, headers *corev3.HeaderMap) (*extprocv3.ProcessingResponse, error) {
	if !p.isUpstreamFilter {
		p.responseHeaders = make(map[string]string, len(headers.GetHeaders()))
		for _, h := range headers.GetHeaders() {
			p.responseHeaders[h.Key] = string(h.GetRawValue())
		}
	}
	return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseHeaders{}}, nil
}

// ProcessResponseBody implements [Processor.ProcessResponseBody]. Successful bodies get their
// backend-native ids re-encoded into gateway ids; error bodies pass through unchanged.
func (p *batchesProcessor) ProcessResponseBody(_ context.Context, body *extprocv3.HttpBody) (*extprocv3.ProcessingResponse, error) {
	if p.isUpstreamFilter || !isSuccessStatus(p.responseHeaders) {
		return passThroughResponseBody(), nil
	}
	workingBody, errResp := p.translatedResponseBody(body.GetBody())
	if errResp != nil {
		return errResp, nil
	}
	var newBody []byte
	var err error
	if p.op == batchesOpList {
		newBody, err = p.reEncodeList(workingBody)
	} else {
		newBody, err = p.reEncodeBatch(workingBody, "")
	}
	if err != nil {
		p.logger.Error("failed to re-encode batches response", slog.String("error", err.Error()))
		return batchesReEncodeError(err.Error()), nil
	}
	return bodyMutationResponse(newBody), nil
}

// translatedResponseBody validates that the response can be re-encoded and runs the translator.
func (p *batchesProcessor) translatedResponseBody(raw []byte) ([]byte, *extprocv3.ProcessingResponse) {
	if p.backend == nil {
		if p.routingModel != "" {
			p.logger.Error("no route matched the batch model; backend unknown", slog.String("model", p.routingModel))
			return nil, batchesReEncodeError(fmt.Sprintf("no backend route found for model %q", p.routingModel))
		}
		return nil, batchesReEncodeError("backend unknown for upstream batches response")
	}
	if len(raw) == 0 {
		return nil, batchesReEncodeError("empty body in upstream batches response")
	}
	if p.translator == nil || p.responseHeaders == nil {
		return raw, nil
	}
	_, mutatedBody, _, _, err := p.translator.ResponseBody(p.responseHeaders, bytes.NewReader(raw), true, nil)
	if err != nil {
		p.logger.Error("failed to translate batches response body", slog.String("error", err.Error()))
		return nil, batchesReEncodeError("failed to process upstream batches response")
	}
	if mutatedBody != nil {
		return mutatedBody, nil
	}
	return raw, nil
}

// encodeID encodes a backend-native id of the given kind into a gateway id for the current backend.
func (p *batchesProcessor) encodeID(kind, nativeID string) (string, error) {
	return p.codec.Encode(&idcodec.BackendID{
		Namespace: p.backend.namespace,
		Name:      p.backend.name,
		Kind:      kind,
		Model:     p.routingModel,
		NativeID:  nativeID,
	})
}

// reEncodeBatch rewrites the batch object found at the gjson path prefix (e.g. "" or "data.3.").
// "id" is required and becomes a gateway batch id; non-empty file id fields become gateway file
// ids. Unknown fields are preserved.
func (p *batchesProcessor) reEncodeBatch(body []byte, prefix string) ([]byte, error) {
	nativeID := gjson.GetBytes(body, prefix+"id").String()
	if nativeID == "" {
		return nil, fmt.Errorf("missing id in upstream batches response")
	}
	gw, err := p.encodeID(idcodec.KindBatch, nativeID)
	if err != nil {
		return nil, fmt.Errorf("failed to encode batch id in upstream batches response")
	}
	if body, err = sjson.SetBytes(body, prefix+"id", gw); err != nil {
		return nil, err
	}
	for _, field := range batchFileIDFields {
		v := gjson.GetBytes(body, prefix+field)
		if v.Type != gjson.String || v.String() == "" {
			continue
		}
		gw, err := p.encodeID(idcodec.KindFile, v.String())
		if err != nil {
			return nil, fmt.Errorf("failed to encode %s in upstream batches response", field)
		}
		if body, err = sjson.SetBytes(body, prefix+field, gw); err != nil {
			return nil, err
		}
	}
	return body, nil
}

// reEncodeList re-encodes every batch in a list page plus first_id/last_id.
func (p *batchesProcessor) reEncodeList(body []byte) ([]byte, error) {
	data := gjson.GetBytes(body, "data")
	if !data.IsArray() {
		return nil, fmt.Errorf("invalid or non-list body in upstream batches list response")
	}
	var err error
	for i := range len(data.Array()) {
		if body, err = p.reEncodeBatch(body, "data."+strconv.Itoa(i)+"."); err != nil {
			return nil, err
		}
	}
	// Never leak backend-native first_id/last_id.
	for _, field := range []string{"first_id", "last_id"} {
		v := gjson.GetBytes(body, field)
		if v.Type != gjson.String || v.String() == "" {
			continue
		}
		gw, err := p.encodeID(idcodec.KindBatch, v.String())
		if err != nil {
			return nil, fmt.Errorf("failed to encode %s in upstream batches list response", field)
		}
		if body, err = sjson.SetBytes(body, field, gw); err != nil {
			return nil, err
		}
	}
	return body, nil
}

// SetBackend implements [Processor.SetBackend]. It enforces the single-backend restriction,
// verifies that a backend decoded from a request id matches the selected backend, records the
// backend used to encode response ids, and resolves the translator. On a retry the last backend wins.
func (p *batchesProcessor) SetBackend(_ context.Context, backend *filterapi.RuntimeBackend, _ string, routeProcessor Processor) error {
	rp, ok := routeProcessor.(*batchesProcessor)
	if !ok {
		panic(fmt.Sprintf("BUG: expected routeProcessor to be *batchesProcessor, got %T", routeProcessor))
	}

	if n := routeRuleBackendCount(rp.config, backend.Backend.Name); n > 1 {
		rp.logger.Error("batch API supports exactly one backend per route rule",
			slog.String("backend", backend.Backend.Name), slog.Int("backends", n))
		return fmt.Errorf("batch API supports exactly one backend per route rule, but the rule serving %q has %d backends", backend.Backend.Name, n)
	}

	ns, name, ok := internalapi.NamespaceAndNameFromBackendName(backend.Backend.Name)
	if !ok {
		return fmt.Errorf("could not parse backend identity %q for batches request", backend.Backend.Name)
	}
	if d := rp.decodedBackend; d != nil && (d.namespace != ns || d.name != name) {
		rp.logger.Error("id belongs to a different backend than the one serving the route",
			slog.String("id_backend", d.namespace+"/"+d.name), slog.String("selected_backend", ns+"/"+name))
		return fmt.Errorf("id belongs to backend %s/%s, but the request was routed to %s/%s", d.namespace, d.name, ns, name)
	}
	rp.backend = &backendKey{namespace: ns, name: name}
	p.routeProcessor = rp

	if err := rp.resolveTranslator(backend.Backend.Schema); err != nil {
		return fmt.Errorf("batch API not supported for schema %s: %w", backend.Backend.Schema.Name, err)
	}
	return nil
}

// resolveTranslator selects the BatchesTranslator for the backend schema and the receiver's op.
func (p *batchesProcessor) resolveTranslator(schema filterapi.VersionedAPISchema) error {
	var t translator.BatchesTranslator
	var err error
	switch p.op {
	case batchesOpCreate:
		t, err = translator.NewBatchCreateTranslator(schema)
	case batchesOpList:
		t, err = translator.NewBatchListTranslator(schema)
	case batchesOpRetrieve:
		t, err = translator.NewBatchRetrieveTranslator(schema)
	case batchesOpCancel:
		t, err = translator.NewBatchCancelTranslator(schema)
	default:
		return fmt.Errorf("resolveTranslator: unsupported operation %d", p.op)
	}
	if err != nil {
		return err
	}
	p.translator = t
	return nil
}

// rewrittenPath reconstructs the request path with the backend-native id in place of the gateway
// id, preserving any configured prefix before the "/v1/batches" marker and the query.
func (p *batchesProcessor) rewrittenPath(op batchesOperation, nativeID string) string {
	pathOnly, query := splitQuery(p.requestHeaders[":path"])
	idx := strings.Index(pathOnly, batchesPathMarker)
	base := pathOnly[:idx+len(batchesPathMarker)]
	suffix := "/" + nativeID
	if op == batchesOpCancel {
		suffix += "/cancel"
	}
	return base + suffix + query
}

// classifyBatchesRequest determines the Batch API operation and (where present) the path id.
func classifyBatchesRequest(method, rawPath string) (op batchesOperation, id string, err error) {
	pathOnly, _ := splitQuery(rawPath)
	idx := strings.Index(pathOnly, batchesPathMarker)
	if idx == -1 {
		return 0, "", fmt.Errorf("not a batches path: %s", rawPath)
	}
	suffix := pathOnly[idx+len(batchesPathMarker):]

	if suffix == "" || suffix == "/" {
		switch method {
		case http.MethodPost:
			return batchesOpCreate, "", nil
		case http.MethodGet:
			return batchesOpList, "", nil
		}
		return 0, "", fmt.Errorf("unsupported method %s for %s", method, rawPath)
	}
	if !strings.HasPrefix(suffix, "/") {
		return 0, "", fmt.Errorf("not a batches path: %s", rawPath)
	}

	segs := strings.Split(suffix[1:], "/")
	switch {
	case len(segs) == 1 && segs[0] != "" && method == http.MethodGet:
		return batchesOpRetrieve, segs[0], nil
	case len(segs) == 2 && segs[0] != "" && segs[1] == "cancel" && method == http.MethodPost:
		return batchesOpCancel, segs[0], nil
	}
	return 0, "", fmt.Errorf("unsupported method %s for %s", method, rawPath)
}

// batchesReEncodeError returns an HTTP 502 ImmediateResponse for responses whose ids cannot be
// safely re-encoded; forwarding them would leak backend-native ids.
func batchesReEncodeError(msg string) *extprocv3.ProcessingResponse {
	return createUserFacingErrorResponse(http.StatusBadGateway, "upstream_error", msg)
}
