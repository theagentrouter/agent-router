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
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
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
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/translator"
)

// filesPathMarker is the canonical Files API path segment. The registered route prefix may be
// preceded by a configurable root/OpenAI prefix, so the operation is derived from the suffix
// that follows this marker rather than from the full path.
const filesPathMarker = "/v1/files"

// uploadModelFormField is the multipart form field (supplied via the OpenAI client's
// `extra_body`) used to select the serving backend for an upload. It is stripped from the
// body before the request is forwarded upstream, since the OpenAI Files API has no model field.
const uploadModelFormField = "model"

// filesOperation identifies which Files API endpoint a request targets.
type filesOperation int

const (
	filesOpUpload   filesOperation = iota // POST /v1/files
	filesOpList                           // GET  /v1/files
	filesOpRetrieve                       // GET  /v1/files/{id}
	filesOpContent                        // GET  /v1/files/{id}/content
	filesOpDelete                         // DELETE /v1/files/{id}
)

// NewFilesProcessorFactory returns a [ProcessorFactory] for the OpenAI Files API endpoints
// ("/v1/files", "/v1/files/{id}", "/v1/files/{id}/content"), using the given backend id codec.
//
// Requests are routed by model. Uploads must send a `model` form field and list requests a
// `?model=` query parameter; other requests use the model stored in the file id. Each route
// must resolve to exactly one backend.
//
//   - Upload: the response id is rewritten to a gateway id encoding the serving backend.
//   - Retrieve/content/delete: the gateway id in the path is decoded and the path rewritten to the
//     backend-native id. The decoded backend must be the one serving the matched route.
//   - List: item ids are re-encoded; an "after" gateway id is decoded back to the native id.
//   - All client-visible ids are gateway-encoded; all ids sent upstream are backend-native.
//
// Encoding the owning backend into the id keeps ids stable if multi-backend (sticky) routing is
// added later.
func NewFilesProcessorFactory(codec idcodec.Codec) ProcessorFactory {
	return func(config *filterapi.RuntimeConfig, requestHeaders map[string]string, logger *slog.Logger, isUpstreamFilter bool, _ bool) (Processor, error) {
		return &filesProcessor{
			codec:            codec,
			config:           config,
			requestHeaders:   requestHeaders,
			logger:           logger,
			isUpstreamFilter: isUpstreamFilter,
		}, nil
	}
}

// filesProcessor implements [Processor] for the Files API at both the router and upstream
// filter levels. The router instance routes the request and rewrites response ids. The upstream
// instance records the selected backend and runs the request translation.
type filesProcessor struct {
	codec            idcodec.Codec
	config           *filterapi.RuntimeConfig
	requestHeaders   map[string]string
	logger           *slog.Logger
	isUpstreamFilter bool

	// Router-side state, established during request processing and consumed during response
	// processing (which is handled at the router filter level).
	op filesOperation
	// backend identifies the owning backend used to (re-)encode response ids; nil means unknown.
	// It is set only by SetBackend from the LB-selected backend.
	backend *backendKey
	// decodedBackend is set when the backend was decoded from a request id (retrieve/content/
	// delete, or a list "after" id). SetBackend verifies the route-selected backend matches it.
	decodedBackend *backendKey
	// routingModel is the model used to route the request. It is recorded in the file ids
	// minted for responses so follow-up requests on those ids match the same route.
	routingModel string

	// translator is the backend's Files translator, set by SetBackend.
	translator translator.FilesTranslator
	// responseHeaders captures the upstream response headers for ResponseBody calls. Populated
	// by ProcessResponseHeaders at the router filter level.
	responseHeaders map[string]string

	// filesRequestMetadata is the upstream request (without body) for translator.RequestBody.
	filesRequestMetadata translator.FilesRequest
	// requestBody is the upload body for translator.RequestBody.
	requestBody []byte

	// routeProcessor is the router processor, set by SetBackend on the upstream filter.
	routeProcessor *filesProcessor
}

var _ Processor = (*filesProcessor)(nil)

// ProcessRequestHeaders implements [Processor.ProcessRequestHeaders].
func (p *filesProcessor) ProcessRequestHeaders(_ context.Context, _ *corev3.HeaderMap) (*extprocv3.ProcessingResponse, error) {
	if p.isUpstreamFilter {
		return p.translateUpstreamRequest()
	}

	method := p.requestHeaders[":method"]
	path := p.requestHeaders[":path"]
	op, id, err := classifyFilesRequest(method, path)
	if err != nil {
		p.logger.Error("unsupported files request", slog.String("method", method), slog.String("path", path))
		return createUserFacingErrorResponse(http.StatusNotFound, "NotFoundError", "unsupported Files API request"), nil
	}
	p.op = op
	p.filesRequestMetadata = translator.FilesRequest{
		Path:        path,
		Method:      method,
		ContentType: p.requestHeaders["content-type"],
	}

	switch op {
	case filesOpUpload:
		// Routing depends on the multipart body; defer to ProcessRequestBody.
		return requestHeadersResponse(p.newRequestHeaderMutation(), false), nil
	case filesOpList:
		return p.handleListRequestHeaders()
	default: // retrieve, content, delete
		return p.handleIDBearingRequestHeaders(op, id)
	}
}

// handleListRequestHeaders sets up routing for GET /v1/files.
//
// A list request carries no model, so it is routed by the "?model=" query parameter. When "after" carries a gateway
// file id (stock SDK pagination passes after=data[-1].id), the model recorded in that id is used
// when "?model=" is absent, and "after" is rewritten to the backend-native id. The routing-only
// "model" parameter is never forwarded upstream.
func (p *filesProcessor) handleListRequestHeaders() (*extprocv3.ProcessingResponse, error) {
	path := p.requestHeaders[":path"]
	headerMutation := p.newRequestHeaderMutation()

	model := queryParam(path, "model")
	upstreamPath := setQueryParam(path, "model", "")

	if after := queryParam(path, "after"); after != "" {
		decoded, err := p.codec.Decode(after)
		if err != nil || decoded.Kind != idcodec.KindFile {
			p.logger.Error("rejecting files list request with undecodable after id", slog.String("after", after))
			return createUserFacingErrorResponse(http.StatusBadRequest, "invalid_request_error", "invalid after cursor"), nil
		}
		p.decodedBackend = &backendKey{namespace: decoded.Namespace, name: decoded.Name}
		if model == "" {
			model = decoded.Model
		}
		upstreamPath = setQueryParam(upstreamPath, "after", decoded.NativeID)
	}

	if model == "" {
		p.logger.Error("list files request is missing the required model query parameter", slog.String("path", path))
		return createUserFacingErrorResponse(http.StatusBadRequest, "invalid_request_error", "the 'model' query parameter is required"), nil
	}
	p.routeByModel(headerMutation, model)
	setHeader(headerMutation, ":path", upstreamPath)
	p.filesRequestMetadata.Path = upstreamPath
	_, p.filesRequestMetadata.Query = splitQuery(upstreamPath)
	return requestHeadersResponse(headerMutation, true), nil
}

// handleIDBearingRequestHeaders decodes a gateway-issued id in the path, routes the request by
// the model recorded in the id, and rewrites the path to the backend-native id. The owning backend
// is verified against the route-selected backend in SetBackend. An undecodable/forged id yields a
// 404.
func (p *filesProcessor) handleIDBearingRequestHeaders(op filesOperation, gatewayID string) (*extprocv3.ProcessingResponse, error) {
	decoded, err := p.codec.Decode(gatewayID)
	if err != nil || decoded.Kind != idcodec.KindFile {
		p.logger.Error("rejecting files request with undecodable id", slog.String("id", gatewayID))
		return createUserFacingErrorResponse(http.StatusNotFound, "NotFoundError", fmt.Sprintf("No such File object: %s", gatewayID)), nil
	}
	p.decodedBackend = &backendKey{namespace: decoded.Namespace, name: decoded.Name}
	p.routingModel = decoded.Model

	headerMutation := p.newRequestHeaderMutation()
	nativePath := p.rewrittenPath(op, decoded.NativeID)
	setHeader(headerMutation, ":path", nativePath)
	p.filesRequestMetadata.NativeID = decoded.NativeID
	p.filesRequestMetadata.Path = nativePath
	if decoded.Model != "" {
		// Route by the model the file was created under, so the request matches the same route.
		p.routeByModel(headerMutation, decoded.Model)
	}
	return requestHeadersResponse(headerMutation, true), nil
}

// newRequestHeaderMutation returns a header mutation that preserves the original (client-facing)
// request path in originalPathHeader, so the upstream filter resolves this processor. Every
// request-phase mutation emitted by the router filter starts from it.
func (p *filesProcessor) newRequestHeaderMutation() *extprocv3.HeaderMutation {
	headerMutation := &extprocv3.HeaderMutation{}
	setHeader(headerMutation, originalPathHeader, p.requestHeaders[":path"])
	return headerMutation
}

// routeByModel records model as the routing model and sets the model header used for route
// matching, both on the outgoing mutation and on the locally tracked request headers.
func (p *filesProcessor) routeByModel(headerMutation *extprocv3.HeaderMutation, model string) {
	p.routingModel = model
	setHeader(headerMutation, internalapi.ModelNameHeaderKeyDefault, model)
	p.requestHeaders[internalapi.ModelNameHeaderKeyDefault] = model
}

// requestHeadersResponse wraps a header mutation in a request-headers processing response.
func requestHeadersResponse(headerMutation *extprocv3.HeaderMutation, clearRouteCache bool) *extprocv3.ProcessingResponse {
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_RequestHeaders{
			RequestHeaders: &extprocv3.HeadersResponse{
				Response: &extprocv3.CommonResponse{
					HeaderMutation:  headerMutation,
					ClearRouteCache: clearRouteCache,
				},
			},
		},
	}
}

// ProcessRequestBody implements [Processor.ProcessRequestBody]. Only the upload operation has a
// request body to process; all other operations are handled at the headers phase.
func (p *filesProcessor) ProcessRequestBody(_ context.Context, rawBody *extprocv3.HttpBody) (*extprocv3.ProcessingResponse, error) {
	if p.isUpstreamFilter || p.op != filesOpUpload {
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestBody{}}, nil
	}

	headerMutation := p.newRequestHeaderMutation()

	boundary := multipartBoundary(p.requestHeaders["content-type"])
	model := ""
	if boundary != "" {
		var params openai.FileNewParams
		if err := params.UnmarshalMultipart(rawBody.Body, boundary); err != nil {
			p.logger.Warn("failed to parse files upload multipart body", slog.String("error", err.Error()))
		} else {
			model = extraBodyModel(&params)
		}
	}

	// The model field is required: it selects the serving backend (AIGatewayRoutes match on the
	// x-ai-eg-model header). Without it the upload cannot be routed, so reject it up front rather
	// than forward an unroutable request.
	if model == "" {
		p.logger.Info("rejecting files upload without required model field")
		return createUserFacingErrorResponse(http.StatusBadRequest, "invalid_request_error", "the 'model' field is required"), nil
	}

	// Route by model and strip the routing-only field from the
	// body before it is forwarded upstream.
	p.routeByModel(headerMutation, model)
	p.filesRequestMetadata.Model = model
	common := &extprocv3.CommonResponse{HeaderMutation: headerMutation, ClearRouteCache: true}
	if stripped, err := stripMultipartField(rawBody.Body, boundary, uploadModelFormField); err != nil {
		p.logger.Warn("failed to strip model field from upload body, forwarding as-is", slog.String("error", err.Error()))
		p.requestBody = rawBody.Body
	} else {
		setHeader(headerMutation, "content-length", strconv.Itoa(len(stripped)))
		common.BodyMutation = &extprocv3.BodyMutation{Mutation: &extprocv3.BodyMutation_Body{Body: stripped}}
		p.requestBody = stripped
	}

	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_RequestBody{RequestBody: &extprocv3.BodyResponse{Response: common}},
	}, nil
}

// ProcessResponseHeaders implements [Processor.ProcessResponseHeaders]. Captures the upstream
// response headers for use by ResponseBody translator calls in ProcessResponseBody.
func (p *filesProcessor) ProcessResponseHeaders(_ context.Context, headers *corev3.HeaderMap) (*extprocv3.ProcessingResponse, error) {
	if p.isUpstreamFilter {
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseHeaders{}}, nil
	}
	// Capture response headers for ResponseBody translator calls.
	p.responseHeaders = make(map[string]string, len(headers.GetHeaders()))
	for _, h := range headers.GetHeaders() {
		p.responseHeaders[h.Key] = string(h.GetRawValue())
	}
	if p.translator == nil {
		// No backend selected (e.g. Envoy local reply).
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseHeaders{}}, nil
	}
	newHeaders, err := p.translator.ResponseHeaders(p.responseHeaders)
	if err != nil {
		return nil, fmt.Errorf("failed to transform files response headers: %w", err)
	}
	if len(newHeaders) == 0 {
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseHeaders{}}, nil
	}
	headerMutation, _ := mutationsFromTranslationResult(newHeaders, nil)
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_ResponseHeaders{
			ResponseHeaders: &extprocv3.HeadersResponse{
				Response: &extprocv3.CommonResponse{HeaderMutation: headerMutation},
			},
		},
	}, nil
}

// ProcessResponseBody implements [Processor.ProcessResponseBody]. Response bodies are processed
// at the router filter level, where the owning backend is known, so client-visible ids can be
// (re-)encoded.
func (p *filesProcessor) ProcessResponseBody(_ context.Context, body *extprocv3.HttpBody) (*extprocv3.ProcessingResponse, error) {
	if p.isUpstreamFilter {
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseBody{}}, nil
	}
	if !p.isSuccessResponse() {
		// Errors have no file id to re-encode; let the translator convert them to OpenAI errors.
		return p.translateResponseError(body.Body)
	}
	switch p.op {
	case filesOpContent:
		// Raw file bytes; no ids to re-encode.
		return p.translateContentResponse(body.Body)
	case filesOpList:
		return p.reEncodeListResponse(body.Body), nil
	default: // upload, retrieve, delete
		return p.reEncodeResponse(body.Body), nil
	}
}

// translateResponseError runs translator.ResponseError on a non-2xx response body.
func (p *filesProcessor) translateResponseError(raw []byte) (*extprocv3.ProcessingResponse, error) {
	if p.translator == nil {
		return passThroughResponseBody(), nil
	}
	newHeaders, newBody, err := p.translator.ResponseError(p.responseHeaders, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("failed to transform files response error: %w", err)
	}
	return translatedBodyResponse(newHeaders, newBody), nil
}

// translateContentResponse runs translator.ResponseBody on a file content download.
func (p *filesProcessor) translateContentResponse(raw []byte) (*extprocv3.ProcessingResponse, error) {
	if p.translator == nil || p.responseHeaders == nil {
		return passThroughResponseBody(), nil
	}
	newHeaders, newBody, _, _, err := p.translator.ResponseBody(p.responseHeaders, bytes.NewReader(raw), true, nil)
	if err != nil {
		p.logger.Error("failed to translate files content body", slog.String("error", err.Error()))
		return filesReEncodeError("failed to process upstream files content response"), nil
	}
	return translatedBodyResponse(newHeaders, newBody), nil
}

// translatedBodyResponse turns a translator result into a response-body mutation.
func translatedBodyResponse(newHeaders []internalapi.Header, newBody []byte) *extprocv3.ProcessingResponse {
	if len(newHeaders) == 0 && newBody == nil {
		return passThroughResponseBody()
	}
	headerMutation, bodyMutation := mutationsFromTranslationResult(newHeaders, newBody)
	if newBody != nil {
		setHeader(headerMutation, "content-length", strconv.Itoa(len(newBody)))
	}
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_ResponseBody{
			ResponseBody: &extprocv3.BodyResponse{
				Response: &extprocv3.CommonResponse{HeaderMutation: headerMutation, BodyMutation: bodyMutation},
			},
		},
	}
}

// isSuccessResponse reports whether the captured upstream response status is 2xx. When the
// status is unknown (no response headers were captured), the response is treated as successful
// so the re-encode path still guards against leaking backend-native ids.
func (p *filesProcessor) isSuccessResponse() bool {
	status, ok := p.responseHeaders[":status"]
	if !ok {
		return true
	}
	code, err := strconv.Atoi(status)
	if err != nil {
		return true
	}
	return code >= 200 && code < 300
}

// reEncodeResponse rewrites the top-level backend-native "id" in a JSON response into a
// gateway-encoded id (upload/retrieve/delete). Returns a 502 ImmediateResponse on any failure.
func (p *filesProcessor) reEncodeResponse(raw []byte) *extprocv3.ProcessingResponse {
	workingBody, errResp := p.translatedResponseBody(raw, "files response")
	if errResp != nil {
		return errResp
	}

	nativeID := gjson.GetBytes(workingBody, "id").String()
	if nativeID == "" {
		p.logger.Error("missing id field; cannot re-encode files response")
		return filesReEncodeError("missing id in upstream files response")
	}
	gw, err := p.encodeFileID(nativeID)
	if err != nil {
		p.logger.Error("failed to re-encode files response id", slog.String("error", err.Error()))
		return filesReEncodeError("failed to encode file id in upstream files response")
	}
	newBody, err := sjson.SetBytes(workingBody, "id", gw)
	if err != nil {
		p.logger.Error("failed to re-encode files response id", slog.String("error", err.Error()))
		return filesReEncodeError("failed to encode file id in upstream files response")
	}
	return bodyMutationResponse(newBody)
}

// translatedResponseBody validates that the response can be re-encoded (backend known, non-empty
// body) and runs the backend translator over it. kind ("files response" / "files list response")
// is used in error messages. On failure it returns a 502 ImmediateResponse.
func (p *filesProcessor) translatedResponseBody(raw []byte, kind string) ([]byte, *extprocv3.ProcessingResponse) {
	if p.backend == nil {
		if p.op == filesOpUpload && p.routingModel != "" {
			p.logger.Error("no route matched the upload model; backend unknown",
				slog.String("model", p.routingModel))
			return nil, filesReEncodeError(fmt.Sprintf(
				"no backend route found for model %q", p.routingModel))
		}
		p.logger.Error("backend unknown; cannot re-encode " + kind)
		return nil, filesReEncodeError("backend unknown for upstream " + kind)
	}
	if len(raw) == 0 {
		p.logger.Error("empty body; cannot re-encode " + kind)
		return nil, filesReEncodeError("empty body in upstream " + kind)
	}
	if p.responseHeaders == nil {
		return raw, nil
	}
	_, mutatedBody, _, _, err := p.translator.ResponseBody(p.responseHeaders, bytes.NewReader(raw), true, nil)
	if err != nil {
		p.logger.Error("failed to translate "+kind+" body", slog.String("error", err.Error()))
		return nil, filesReEncodeError("failed to process upstream " + kind)
	}
	if mutatedBody != nil {
		return mutatedBody, nil
	}
	return raw, nil
}

// encodeFileID encodes a backend-native file id into a gateway file id for the current backend.
func (p *filesProcessor) encodeFileID(nativeID string) (string, error) {
	return p.codec.Encode(&idcodec.BackendID{
		Namespace: p.backend.namespace,
		Name:      p.backend.name,
		Kind:      idcodec.KindFile,
		Model:     p.routingModel,
		NativeID:  nativeID,
	})
}

// reEncodeListResponse re-encodes every data[].id (and first_id/last_id) of a single-backend list
// page into gateway file ids. has_more is passed through unchanged. Returns a 502
// ImmediateResponse on any failure.
func (p *filesProcessor) reEncodeListResponse(raw []byte) *extprocv3.ProcessingResponse {
	workingBody, errResp := p.translatedResponseBody(raw, "files list response")
	if errResp != nil {
		return errResp
	}

	// An upstream error (or any non-list body) either fails to unmarshal or has no data array;
	// return an error so the client is not misled by a response that cannot be safely re-encoded.
	var page openai.FileListResponse
	if err := json.Unmarshal(workingBody, &page); err != nil || page.Data == nil {
		p.logger.Error("failed to unmarshal files list response body; cannot re-encode ids")
		return filesReEncodeError("invalid or non-list body in upstream files list response")
	}

	for i := range page.Data {
		if page.Data[i].ID == "" {
			continue
		}
		gw, err := p.encodeFileID(page.Data[i].ID)
		if err != nil {
			p.logger.Error("failed to re-encode files list ids", slog.String("error", err.Error()))
			return filesReEncodeError("failed to encode file id in upstream files list response")
		}
		page.Data[i].ID = gw
	}
	// Never leak backend-native first_id/last_id.
	for _, f := range []*string{&page.FirstID, &page.LastID} {
		if *f == "" {
			continue
		}
		gw, err := p.encodeFileID(*f)
		if err != nil {
			p.logger.Error("failed to re-encode files list cursor ids", slog.String("error", err.Error()))
			return filesReEncodeError("failed to encode file id in upstream files list response")
		}
		*f = gw
	}

	newBody, err := json.Marshal(page)
	if err != nil {
		p.logger.Error("failed to marshal files list response", slog.String("error", err.Error()))
		return filesReEncodeError("failed to encode upstream files list response")
	}
	return bodyMutationResponse(newBody)
}

// SetBackend implements [Processor.SetBackend]. It is called on the upstream filter instance
// with the LB-selected backend and a reference to the router instance. It enforces the
// single-backend restriction, verifies that a backend decoded from a request id matches the
// selected backend, records the backend used to encode response ids, and resolves the translator.
// It is called once per attempt, so on a retry the last (response-serving) backend wins.
func (p *filesProcessor) SetBackend(_ context.Context, backend *filterapi.RuntimeBackend, _ string, routeProcessor Processor) error {
	rp, ok := routeProcessor.(*filesProcessor)
	if !ok {
		panic(fmt.Sprintf("BUG: expected routeProcessor to be *filesProcessor, got %T", routeProcessor))
	}

	if n := routeRuleBackendCount(rp.config, backend.Backend.Name); n > 1 {
		rp.logger.Error("files API supports exactly one backend per route rule",
			slog.String("backend", backend.Backend.Name), slog.Int("backends", n))
		return fmt.Errorf("files API supports exactly one backend per route rule, but the rule serving %q has %d backends", backend.Backend.Name, n)
	}

	ns, name, ok := internalapi.NamespaceAndNameFromBackendName(backend.Backend.Name)
	if !ok {
		return fmt.Errorf("could not parse backend identity %q for files request", backend.Backend.Name)
	}
	if d := rp.decodedBackend; d != nil && (d.namespace != ns || d.name != name) {
		rp.logger.Error("file id belongs to a different backend than the one serving the route",
			slog.String("id_backend", d.namespace+"/"+d.name), slog.String("selected_backend", ns+"/"+name))
		return fmt.Errorf("file id belongs to backend %s/%s, but the request was routed to %s/%s", d.namespace, d.name, ns, name)
	}
	rp.backend = &backendKey{namespace: ns, name: name}

	if err := rp.resolveTranslator(backend.Backend.Schema); err != nil {
		rp.logger.Info("unsupported schema for files operation", slog.String("schema", string(backend.Backend.Schema.Name)), slog.Int("op", int(rp.op)))
		return fmt.Errorf("files API not supported for schema %s: %w", backend.Backend.Schema.Name, err)
	}
	p.routeProcessor = rp
	return nil
}

// translateUpstreamRequest runs translator.RequestBody at the upstream filter, after SetBackend,
// and applies the returned headers and body.
func (p *filesProcessor) translateUpstreamRequest() (*extprocv3.ProcessingResponse, error) {
	rp := p.routeProcessor
	if rp == nil || rp.translator == nil {
		// No backend resolved.
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestHeaders{}}, nil
	}
	newHeaders, newBody, err := rp.translator.RequestBody(rp.requestBody, &rp.filesRequestMetadata, false)
	if err != nil {
		if userFacingErr := internalapi.GetUserFacingError(err); userFacingErr != nil {
			p.logger.Info("returning user-facing error for invalid files request", slog.String("error", err.Error()))
			return createUserFacingErrorResponse(http.StatusUnprocessableEntity, "UnprocessableEntity", userFacingErr.Error()), nil
		}
		return nil, fmt.Errorf("failed to transform files request: %w", err)
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

// backendKey identifies a backend by its AIServiceBackend namespace/name.
type backendKey struct {
	namespace string
	name      string
}

// routeRuleBackendCount returns how many backends in config belong to the same AIGatewayRoute rule
// as the given composite backend name ("<ns>/<name>/route/<route>/rule/<i>/ref/<j>", see
// [internalapi.PerRouteRuleRefBackendName]). It returns 0 when the name is not in that form.
func routeRuleBackendCount(config *filterapi.RuntimeConfig, composite string) int {
	key, ok := routeRuleKey(composite)
	if !ok || config == nil {
		return 0
	}
	n := 0
	for other := range config.Backends {
		if k, ok := routeRuleKey(other); ok && k == key {
			n++
		}
	}
	return n
}

// routeRuleKey extracts the "route/<route>/rule/<i>" part of a composite per-route-rule backend name.
func routeRuleKey(composite string) (string, bool) {
	ri := strings.Index(composite, "/route/")
	if ri == -1 {
		return "", false
	}
	rest := composite[ri+1:]
	ref := strings.LastIndex(rest, "/ref/")
	if ref == -1 {
		return "", false
	}
	return rest[:ref], true
}

// rewrittenPath reconstructs the request path with the backend-native id in place of the
// gateway id, preserving any configured prefix before the "/v1/files" marker and the query.
func (p *filesProcessor) rewrittenPath(op filesOperation, nativeID string) string {
	pathOnly, query := splitQuery(p.requestHeaders[":path"])
	idx := strings.Index(pathOnly, filesPathMarker)
	base := pathOnly[:idx+len(filesPathMarker)]
	suffix := "/" + nativeID
	if op == filesOpContent {
		suffix += "/content"
	}
	return base + suffix + query
}

// classifyFilesRequest determines the Files API operation and (where present) the path id from
// the request method and path.
func classifyFilesRequest(method, rawPath string) (op filesOperation, id string, err error) {
	pathOnly, _ := splitQuery(rawPath)
	idx := strings.Index(pathOnly, filesPathMarker)
	if idx == -1 {
		return 0, "", fmt.Errorf("not a files path: %s", rawPath)
	}
	suffix := pathOnly[idx+len(filesPathMarker):]

	if suffix == "" || suffix == "/" {
		switch method {
		case http.MethodPost:
			return filesOpUpload, "", nil
		case http.MethodGet:
			return filesOpList, "", nil
		}
		return 0, "", fmt.Errorf("unsupported method %s for %s", method, rawPath)
	}

	segs := strings.Split(strings.TrimPrefix(suffix, "/"), "/")
	switch {
	case len(segs) == 1 && segs[0] != "":
		switch method {
		case http.MethodGet:
			return filesOpRetrieve, segs[0], nil
		case http.MethodDelete:
			return filesOpDelete, segs[0], nil
		}
	case len(segs) == 2 && segs[0] != "" && segs[1] == "content":
		if method == http.MethodGet {
			return filesOpContent, segs[0], nil
		}
	}
	return 0, "", fmt.Errorf("unsupported method %s for %s", method, rawPath)
}

// passThroughResponseBody returns a response-body processing result that leaves the body
// unmodified.
func passThroughResponseBody() *extprocv3.ProcessingResponse {
	return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseBody{}}
}

// filesReEncodeError returns an ImmediateResponse (HTTP 502) for cases where a file id could not
// be safely re-encoded. Forwarding the upstream body unchanged in these cases would leak
// backend-native ids to the client, so a controlled error is returned instead. It is returned with
// a nil Go error so the ext_proc stream stays intact.
func filesReEncodeError(msg string) *extprocv3.ProcessingResponse {
	return createUserFacingErrorResponse(http.StatusBadGateway, "upstream_error", msg)
}

// bodyMutationResponse returns a response-body processing result that replaces the body with
// newBody and updates content-length accordingly.
func bodyMutationResponse(newBody []byte) *extprocv3.ProcessingResponse {
	headerMutation := &extprocv3.HeaderMutation{}
	setHeader(headerMutation, "content-length", strconv.Itoa(len(newBody)))
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_ResponseBody{
			ResponseBody: &extprocv3.BodyResponse{
				Response: &extprocv3.CommonResponse{
					HeaderMutation: headerMutation,
					BodyMutation:   &extprocv3.BodyMutation{Mutation: &extprocv3.BodyMutation_Body{Body: newBody}},
				},
			},
		},
	}
}

// setQueryParam returns path with the named query parameter set to value, or removed when value
// is empty. All other query parameters are preserved. When removing a parameter that is absent
// (or when there is no query), path is returned unchanged. On a malformed query path is returned
// unchanged. It drops the routing-only "model" parameter and rewrites the "after" cursor to the
// backend-native id before a Files list request is forwarded upstream.
func setQueryParam(path, name, value string) string {
	pathOnly, rawQuery := splitQuery(path)
	values, err := url.ParseQuery(strings.TrimPrefix(rawQuery, "?"))
	if err != nil {
		return path
	}
	if value == "" {
		if _, ok := values[name]; !ok {
			return path
		}
		values.Del(name)
	} else {
		values.Set(name, value)
	}
	if encoded := values.Encode(); encoded != "" {
		return pathOnly + "?" + encoded
	}
	return pathOnly
}

// multipartBoundary extracts the boundary from a multipart/form-data content-type header,
// returning "" if the content type is not multipart or has no boundary.
func multipartBoundary(contentType string) string {
	if contentType == "" {
		return ""
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		return ""
	}
	return params["boundary"]
}

// extraBodyModel returns the routing model carried in the upload's `extra_body` (multipart
// `model` form field). UnmarshalMultipart stores unknown fields as []byte.
func extraBodyModel(params *openai.FileNewParams) string {
	switch v := params.ExtraBody[uploadModelFormField].(type) {
	case []byte:
		return string(v)
	case string:
		return v
	default:
		return ""
	}
}

// stripMultipartField returns a copy of a multipart/form-data body with the named field removed,
// preserving the original boundary and all other parts (including the uploaded file bytes).
func stripMultipartField(body []byte, boundary, field string) ([]byte, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.SetBoundary(boundary); err != nil {
		return nil, err
	}
	r := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := r.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if part.FormName() == field {
			continue
		}
		pw, err := w.CreatePart(part.Header)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(pw, part); err != nil { //nolint:gosec // bounded by Envoy's buffered body size limit
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// splitQuery splits a request path into its path and "?query" (the query includes the leading
// "?" or is empty).
func splitQuery(path string) (pathOnly, query string) {
	if i := strings.Index(path, "?"); i != -1 {
		return path[:i], path[i:]
	}
	return path, ""
}

// queryParam returns the first (URL-decoded) value of the named query parameter in a request
// path, or "".
func queryParam(path, name string) string {
	_, raw := splitQuery(path)
	raw = strings.TrimPrefix(raw, "?")
	values, err := url.ParseQuery(raw)
	if err != nil {
		return ""
	}
	return values.Get(name)
}

// resolveTranslator selects and stores the appropriate FilesTranslator for the given backend
// schema and the receiver's op.
func (p *filesProcessor) resolveTranslator(schema filterapi.VersionedAPISchema) error {
	var t translator.FilesTranslator
	var err error
	switch p.op {
	case filesOpUpload:
		t, err = translator.NewFileUploadTranslator(schema)
	case filesOpList:
		t, err = translator.NewFileListTranslator(schema)
	case filesOpRetrieve:
		t, err = translator.NewFileRetrieveTranslator(schema)
	case filesOpContent:
		t, err = translator.NewFileContentTranslator(schema)
	case filesOpDelete:
		t, err = translator.NewFileDeleteTranslator(schema)
	default:
		return fmt.Errorf("resolveTranslator: unsupported operation %d", p.op)
	}
	if err != nil {
		return err
	}
	if t == nil {
		return fmt.Errorf("resolveTranslator: nil translator for operation %d schema %s", p.op, schema.Name)
	}
	p.translator = t
	return nil
}
