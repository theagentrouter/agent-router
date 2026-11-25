// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"cmp"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/fal"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

const (
	falBillableUnitsHeader = "x-fal-billable-units"
	// maxFalBillableUnits keeps uint(billable_units * 1e6) in cost CEL inside the uint64 range.
	maxFalBillableUnits = 1e12
)

// NewFalToFalTranslator implements [Factory] for fal run and queue passthrough.
func NewFalToFalTranslator(prefix, scheme string) FalTranslator {
	return &falToFalTranslator{prefix: prefix, scheme: scheme}
}

// falToFalTranslator forwards fal requests with the gateway path rewritten to fal's, and points fal
// URLs in responses back at the gateway. The fal host comes from the backend the route selected.
type falToFalTranslator struct {
	prefix               string
	scheme               string
	requestHeaders       map[string]string
	clientRequestHeaders map[string]string
	requestModel         internalapi.RequestModel
	stream               bool
	billed               bool
	sse                  *fal.SSERewriter
	sseLine              []byte
	streamUsage          metrics.TokenUsage
}

// SetRequestHeaders implements [RequestHeadersSetter].
func (t *falToFalTranslator) SetRequestHeaders(headers map[string]string) {
	t.requestHeaders = headers
}

// SetClientRequestHeaders implements [ClientRequestHeadersSetter].
func (t *falToFalTranslator) SetClientRequestHeaders(headers map[string]string) {
	t.clientRequestHeaders = headers
}

// RequestBody implements [FalTranslator.RequestBody].
func (t *falToFalTranslator) RequestBody(original []byte, _ *fal.Request, forceBodyMutation bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	originalPath := t.requestHeaders[internalapi.OriginalPathHeader]
	p, ok := fal.ParsePath(t.prefix, originalPath)
	if !ok {
		return nil, nil, fmt.Errorf("not a fal path: %q", originalPath)
	}
	t.requestModel = t.requestHeaders[internalapi.ModelNameHeaderKeyDefault]
	t.stream = p.Stream
	t.billed = p.Billed
	newHeaders = []internalapi.Header{{pathHeaderName, stripFalWebhook(p.Upstream)}}
	if forceBodyMutation && len(original) > 0 {
		newBody = original
		newHeaders = append(newHeaders, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))})
	}
	return
}

// stripFalWebhook drops fal_webhook from the query so fal cannot deliver results around the gateway.
func stripFalWebhook(path string) string {
	base, query, ok := strings.Cut(path, "?")
	if !ok {
		return path
	}
	params := strings.Split(query, "&")
	kept := params[:0]
	for _, param := range params {
		key, _, _ := strings.Cut(param, "=")
		if k, err := url.QueryUnescape(key); err == nil && k == "fal_webhook" {
			continue
		}
		kept = append(kept, param)
	}
	if len(kept) == 0 {
		return base
	}
	return base + "?" + strings.Join(kept, "&")
}

// ResponseHeaders implements [FalTranslator.ResponseHeaders].
func (t *falToFalTranslator) ResponseHeaders(map[string]string) ([]internalapi.Header, error) {
	return nil, nil
}

// ResponseBody implements [FalTranslator.ResponseBody].
func (t *falToFalTranslator) ResponseBody(respHeaders map[string]string, body io.Reader, endOfStream bool, span tracingapi.FalSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	responseModel = t.requestModel
	if units, perr := strconv.ParseFloat(strings.TrimSpace(respHeaders[falBillableUnitsHeader]), 64); t.billed && perr == nil && units >= 0 && units <= maxFalBillableUnits {
		tokenUsage.SetBillableUnits(units)
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, tokenUsage, responseModel, fmt.Errorf("failed to read fal response body: %w", err)
	}
	run, queue, ok := fal.GatewayBases(t.prefix, t.clientRequestHeaders[internalapi.OriginalPathHeader],
		t.clientRequestHeaders[":authority"], t.gatewayScheme())

	if t.stream && respHeaders[":status"] == "200" {
		t.setStreamUsage(&tokenUsage, raw, endOfStream)
		t.streamUsage.Override(tokenUsage)
		if span != nil && endOfStream {
			span.RecordResponse(&fal.Response{Model: responseModel, Usage: falSpanUsage(&t.streamUsage)})
		}
		if !ok {
			return nil, nil, tokenUsage, responseModel, nil
		}
		if t.sse == nil {
			t.sse = fal.NewSSERewriter(run, queue)
		}
		return nil, t.sse.Write(raw, endOfStream), tokenUsage, responseModel, nil
	}

	setFalBodyUsage(&tokenUsage, raw)
	if span != nil {
		span.RecordResponse(&fal.Response{Model: responseModel, Body: raw, Usage: falSpanUsage(&tokenUsage)})
	}
	if !ok {
		return nil, nil, tokenUsage, responseModel, nil
	}
	if rewritten := fal.RewriteURLs(raw, run, queue); !bytes.Equal(rewritten, raw) {
		newBody = rewritten
		newHeaders = []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(newBody))}}
	}
	return
}

// gatewayScheme returns the scheme setting if set, else the client's first x-forwarded-proto, else its :scheme.
// A client-sent value other than http or https is ignored, so it can never reach a rewritten URL.
func (t *falToFalTranslator) gatewayScheme() string {
	if t.scheme != "" {
		return t.scheme
	}
	proto, _, _ := strings.Cut(t.clientRequestHeaders["x-forwarded-proto"], ",")
	return cmp.Or(fal.ValidScheme(strings.TrimSpace(proto)), fal.ValidScheme(t.clientRequestHeaders[":scheme"]))
}

// setStreamUsage applies setFalBodyUsage to each complete SSE data line, holding back a partial last line.
func (t *falToFalTranslator) setStreamUsage(u *metrics.TokenUsage, chunk []byte, endOfStream bool) {
	t.sseLine = append(t.sseLine, chunk...)
	end := bytes.LastIndexByte(t.sseLine, '\n')
	if endOfStream {
		end = len(t.sseLine)
	}
	if end < 0 {
		return
	}
	for _, line := range bytes.Split(t.sseLine[:end], []byte("\n")) {
		if data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:")); ok {
			setFalBodyUsage(u, bytes.TrimSpace(data))
		}
	}
	t.sseLine = append(t.sseLine[:0], t.sseLine[min(end+1, len(t.sseLine)):]...)
}

// setFalBodyUsage records the token counts of a top-level usage object, as LLM endpoints such as
// openrouter/router return.
func setFalBodyUsage(u *metrics.TokenUsage, body []byte) {
	usage := gjson.GetBytes(body, "usage")
	if !usage.IsObject() {
		return
	}
	if v := usage.Get("prompt_tokens"); v.Exists() {
		u.SetInputTokens(uint32(v.Uint())) //nolint:gosec
	}
	if v := usage.Get("completion_tokens"); v.Exists() {
		u.SetOutputTokens(uint32(v.Uint())) //nolint:gosec
	}
	if v := usage.Get("total_tokens"); v.Exists() {
		u.SetTotalTokens(uint32(v.Uint())) //nolint:gosec
	}
}

// falSpanUsage returns the token counts in u for trace spans, or nil when it has none.
func falSpanUsage(u *metrics.TokenUsage) *fal.Usage {
	in, inOK := u.InputTokens()
	out, outOK := u.OutputTokens()
	if !inOK && !outOK {
		return nil
	}
	usage := &fal.Usage{}
	if inOK {
		usage.InputTokens = ptr.To(int(in))
	}
	if outOK {
		usage.OutputTokens = ptr.To(int(out))
	}
	return usage
}

// ResponseError implements [FalTranslator.ResponseError].
func (t *falToFalTranslator) ResponseError(_ map[string]string, body io.Reader) (newHeaders []internalapi.Header, newBody []byte, err error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read fal error body: %w", err)
	}
	run, queue, ok := fal.GatewayBases(t.prefix, t.clientRequestHeaders[internalapi.OriginalPathHeader],
		t.clientRequestHeaders[":authority"], t.gatewayScheme())
	if !ok {
		return nil, nil, nil
	}
	if rewritten := fal.RewriteURLs(raw, run, queue); !bytes.Equal(rewritten, raw) {
		newBody = rewritten
		newHeaders = []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(newBody))}}
	}
	return
}
