// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"maps"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/fal"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
)

func TestFalToFalTranslator_RequestBody(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string]string
		raw        []byte
		forceBody  bool
		expHeaders []internalapi.Header
		expBody    []byte
		expErr     string
	}{
		{
			name: "run path is stripped of the gateway prefix",
			headers: map[string]string{
				internalapi.OriginalPathHeader:        "/fal/run/fal-ai/flux/dev",
				internalapi.ModelNameHeaderKeyDefault: "fal-ai/flux/dev",
			},
			raw:        []byte(`{"prompt":"a cat"}`),
			expHeaders: []internalapi.Header{{pathHeaderName, "/fal-ai/flux/dev"}},
		},
		{
			name: "queue poll keeps request id and query",
			headers: map[string]string{
				internalapi.OriginalPathHeader:        "/fal/queue/fal-ai/flux/requests/7f1c/status?logs=1",
				internalapi.ModelNameHeaderKeyDefault: "fal-ai/flux",
			},
			expHeaders: []internalapi.Header{{pathHeaderName, "/fal-ai/flux/requests/7f1c/status?logs=1"}},
		},
		{
			name: "fal_webhook removed from queue submit",
			headers: map[string]string{
				internalapi.OriginalPathHeader:        "/fal/queue/fal-ai/flux/dev?b=2&fal_webhook=https%3A%2F%2Fevil.example%2Fhook&a=1&fal%5Fwebhook=x",
				internalapi.ModelNameHeaderKeyDefault: "fal-ai/flux/dev",
			},
			raw:        []byte(`{"prompt":"a cat"}`),
			expHeaders: []internalapi.Header{{pathHeaderName, "/fal-ai/flux/dev?b=2&a=1"}},
		},
		{
			name: "path without fal_webhook unchanged",
			headers: map[string]string{
				internalapi.OriginalPathHeader:        "/fal/queue/fal-ai/flux/dev?b=2&a=1&webhook=keep",
				internalapi.ModelNameHeaderKeyDefault: "fal-ai/flux/dev",
			},
			raw:        []byte(`{"prompt":"a cat"}`),
			expHeaders: []internalapi.Header{{pathHeaderName, "/fal-ai/flux/dev?b=2&a=1&webhook=keep"}},
		},
		{
			name: "fal_webhook as only query param drops the query",
			headers: map[string]string{
				internalapi.OriginalPathHeader:        "/fal/queue/fal-ai/flux/dev?fal_webhook=https://evil.example/hook",
				internalapi.ModelNameHeaderKeyDefault: "fal-ai/flux/dev",
			},
			raw:        []byte(`{"prompt":"a cat"}`),
			expHeaders: []internalapi.Header{{pathHeaderName, "/fal-ai/flux/dev"}},
		},
		{
			name: "forced body mutation replays the original body",
			headers: map[string]string{
				internalapi.OriginalPathHeader: "/fal/run/fal-ai/flux/dev",
			},
			raw:       []byte(`{"prompt":"a cat"}`),
			forceBody: true,
			expHeaders: []internalapi.Header{
				{pathHeaderName, "/fal-ai/flux/dev"},
				{contentLengthHeaderName, "18"},
			},
			expBody: []byte(`{"prompt":"a cat"}`),
		},
		{
			name:    "missing original path",
			headers: map[string]string{},
			expErr:  "not a fal path",
		},
		{
			name:    "non-fal original path",
			headers: map[string]string{internalapi.OriginalPathHeader: "/v1/chat/completions"},
			expErr:  "not a fal path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := NewFalToFalTranslator("/fal", internalapi.FalURLSchemeHTTPS)
			tr.(RequestHeadersSetter).SetRequestHeaders(tt.headers)
			headers, body, err := tr.RequestBody(tt.raw, &fal.Request{}, tt.forceBody)
			if tt.expErr != "" {
				require.ErrorContains(t, err, tt.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.expHeaders, headers)
			require.Equal(t, tt.expBody, body)
		})
	}
}

func TestFalToFalTranslator_Response(t *testing.T) {
	tests := []struct {
		name  string
		model string
		body  string
		eos   bool
	}{
		{name: "json result", model: "fal-ai/flux/dev", body: `{"images":[{"url":"https://v3.fal.media/x.png"}]}`, eos: true},
		{name: "sse chunk", model: "fal-ai/flux/dev", body: "data: {\"status\":\"IN_PROGRESS\"}\n\n"},
		{name: "poll", model: "fal-ai/flux", body: `{"status":"COMPLETED"}`, eos: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := NewFalToFalTranslator("/fal", internalapi.FalURLSchemeHTTPS)
			tr.(RequestHeadersSetter).SetRequestHeaders(map[string]string{
				internalapi.OriginalPathHeader:        "/fal/run/" + tt.model,
				internalapi.ModelNameHeaderKeyDefault: tt.model,
			})
			_, _, err := tr.RequestBody(nil, &fal.Request{}, false)
			require.NoError(t, err)

			respHeaders, err := tr.ResponseHeaders(map[string]string{":status": "200"})
			require.NoError(t, err)
			require.Nil(t, respHeaders)

			headers, body, usage, model, err := tr.ResponseBody(nil, bytes.NewBufferString(tt.body), tt.eos, nil)
			require.NoError(t, err)
			require.Nil(t, headers)
			require.Nil(t, body)
			require.Equal(t, metrics.TokenUsage{}, usage)
			require.Equal(t, tt.model, model)

			headers, body, err = tr.ResponseError(map[string]string{":status": "422"}, bytes.NewBufferString(`{"detail":"bad"}`))
			require.NoError(t, err)
			require.Nil(t, headers)
			require.Nil(t, body)
		})
	}
}

func TestFalToFalTranslator_ResponseBody(t *testing.T) {
	units := func(v float64) metrics.TokenUsage {
		var u metrics.TokenUsage
		u.SetBillableUnits(v)
		return u
	}
	contentLength := func(body string) []internalapi.Header {
		return []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(body))}}
	}
	var llmUsage metrics.TokenUsage
	llmUsage.SetInputTokens(40)
	llmUsage.SetOutputTokens(227)
	llmUsage.SetTotalTokens(267)

	const (
		submitFal = `{"request_id":"r","response_url":"https://queue.fal.run/fal-ai/flux/requests/r",` +
			`"status_url":"https://queue.fal.run/fal-ai/flux/requests/r/status",` +
			`"cancel_url":"https://queue.fal.run/fal-ai/flux/requests/r/cancel","queue_position":0}`
		submitGtw = `{"request_id":"r","response_url":"https://gw.example/fal/queue/fal-ai/flux/requests/r",` +
			`"status_url":"https://gw.example/fal/queue/fal-ai/flux/requests/r/status",` +
			`"cancel_url":"https://gw.example/fal/queue/fal-ai/flux/requests/r/cancel","queue_position":0}`
		statusGtw = `{"status":"COMPLETED","response_url":"https://gw.example/fal/queue/fal-ai/flux/requests/r"}`
		errGtw    = `{"detail":"Request is still in progress","status_url":"https://gw.example/fal/queue/fal-ai/flux/requests/r/status"}`
	)
	tests := []struct {
		name       string
		path       string
		scheme     string
		clientHdrs map[string]string
		respHdrs   map[string]string
		chunks     []string
		expHeaders []internalapi.Header
		expBody    []byte
		expUsage   metrics.TokenUsage
	}{
		{
			name:     "billable units header recorded",
			path:     "/fal/run/fal-ai/flux/dev",
			respHdrs: map[string]string{":status": "200", "x-fal-billable-units": "1.5"},
			chunks:   []string{`{"images":[{"url":"https://v3.fal.media/files/a.png"}]}`},
			expUsage: units(1.5),
		},
		{
			name:     "missing header gives zero",
			path:     "/fal/run/fal-ai/flux/dev",
			respHdrs: map[string]string{":status": "200"},
			chunks:   []string{`{"images":[{"url":"https://v3.fal.media/files/a.png"}]}`},
		},
		{
			name:     "unparsable header gives zero",
			path:     "/fal/run/fal-ai/flux/dev",
			respHdrs: map[string]string{":status": "200", "x-fal-billable-units": "abc"},
			chunks:   []string{`{"images":[]}`},
		},
		{
			name:     "negative billable units header ignored",
			path:     "/fal/run/fal-ai/flux/dev",
			respHdrs: map[string]string{":status": "200", "x-fal-billable-units": "-1"},
			chunks:   []string{`{"images":[]}`},
		},
		{
			name:     "nan billable units header ignored",
			path:     "/fal/run/fal-ai/flux/dev",
			respHdrs: map[string]string{":status": "200", "x-fal-billable-units": "NaN"},
			chunks:   []string{`{"images":[]}`},
		},
		{
			name:     "billable units above the cost CEL range ignored",
			path:     "/fal/run/fal-ai/flux/dev",
			respHdrs: map[string]string{":status": "200", "x-fal-billable-units": "1e30"},
			chunks:   []string{`{"images":[]}`},
		},
		{
			name:     "infinite billable units header ignored",
			path:     "/fal/run/fal-ai/flux/dev",
			respHdrs: map[string]string{":status": "200", "x-fal-billable-units": "1e500"},
			chunks:   []string{`{"images":[]}`},
		},
		{
			name:     "billable units header with surrounding spaces recorded",
			path:     "/fal/queue/fal-ai/flux/requests/7f1c",
			respHdrs: map[string]string{":status": "200", "x-fal-billable-units": " 1 "},
			chunks:   []string{`{"images":[]}`},
			expUsage: units(1),
		},
		{
			name:     "billable units on a queue submit ignored",
			path:     "/fal/queue/fal-ai/flux/dev",
			respHdrs: map[string]string{":status": "200", "x-fal-billable-units": "1"},
			chunks:   []string{`{"request_id":"7f1c"}`},
		},
		{
			name:     "billable units on a queue status ignored",
			path:     "/fal/queue/fal-ai/flux/requests/7f1c/status",
			respHdrs: map[string]string{":status": "200", "x-fal-billable-units": "1"},
			chunks:   []string{`{"status":"COMPLETED"}`},
		},
		{
			name:     "billable units on a queue cancel ignored",
			path:     "/fal/queue/fal-ai/flux/requests/7f1c/cancel",
			respHdrs: map[string]string{":status": "200", "x-fal-billable-units": "1"},
			chunks:   []string{`{"status":"CANCELLATION_REQUESTED"}`},
		},
		{
			name:     "streamed body usage tokens recorded once",
			path:     "/fal/run/openrouter/router/stream",
			respHdrs: map[string]string{":status": "200"},
			chunks: []string{
				`data: {"output":"h"}` + "\n\n" + `data: {"output":"hi","usage":{"prompt_tok`,
				`ens":40,"completion_tokens":227,"total_tokens":267}}` + "\n\n",
			},
			expBody:  []byte(`data: {"output":"h"}` + "\n\n" + `data: {"output":"hi","usage":{"prompt_tokens":40,"completion_tokens":227,"total_tokens":267}}` + "\n\n"),
			expUsage: llmUsage,
		},
		{
			name:     "streamed usage tokens recorded",
			path:     "/fal/run/openrouter/router/stream",
			respHdrs: map[string]string{":status": "200"},
			chunks: []string{
				`data: {"output":"h","partial":true,"usage":null}` + "\n\n",
				`data: {"output":"hi","partial":false,"usage":{"prompt_tokens":40,"completion_tokens":227,"total_tokens":267}}` + "\n\n",
			},
			expBody:  []byte(`data: {"output":"h","partial":true,"usage":null}` + "\n\n" + `data: {"output":"hi","partial":false,"usage":{"prompt_tokens":40,"completion_tokens":227,"total_tokens":267}}` + "\n\n"),
			expUsage: llmUsage,
		},
		{
			name:     "billable units header recorded",
			path:     "/fal/run/openrouter/router/stream",
			respHdrs: map[string]string{":status": "200", "x-fal-billable-units": "1.5"},
			chunks:   []string{`data: {"output":"hi","partial":false}` + "\n\n"},
			expBody:  []byte(`data: {"output":"hi","partial":false}` + "\n\n"),
			expUsage: units(1.5),
		},
		{
			name:     "body usage tokens recorded",
			path:     "/fal/run/openrouter/router",
			respHdrs: map[string]string{":status": "200"},
			chunks:   []string{`{"output":"hi","usage":{"prompt_tokens":40,"completion_tokens":227,"total_tokens":267}}`},
			expUsage: llmUsage,
		},
		{
			name:       "queue submit response urls point at the gateway",
			path:       "/fal/queue/fal-ai/flux/dev",
			respHdrs:   map[string]string{":status": "200", "x-fal-billable-units": "0"},
			chunks:     []string{submitFal},
			expHeaders: contentLength(submitGtw),
			expBody:    []byte(submitGtw),
		},
		{
			name:       "status response urls point at the gateway",
			path:       "/fal/queue/fal-ai/flux/requests/r/status?logs=1",
			respHdrs:   map[string]string{":status": "200"},
			chunks:     []string{`{"status":"COMPLETED","response_url":"https://queue.fal.run/fal-ai/flux/requests/r"}`},
			expHeaders: contentLength(statusGtw),
			expBody:    []byte(statusGtw),
		},
		{
			name:     "streamed status event urls point at the gateway",
			path:     "/fal/queue/fal-ai/flux/requests/r/status/stream",
			respHdrs: map[string]string{":status": "200"},
			chunks: []string{
				`data: {"status":"IN_PROGRESS","status_url":"https://queue.fa`,
				"l.run/fal-ai/flux/requests/r/status\"}\n\n",
			},
			expBody: []byte("data: {\"status\":\"IN_PROGRESS\",\"status_url\":\"https://gw.example/fal/queue/fal-ai/flux/requests/r/status\"}\n\n"),
		},
		{
			name:     "run response body without urls unchanged",
			path:     "/fal/run/fal-ai/flux/dev",
			respHdrs: map[string]string{":status": "200"},
			chunks:   []string{`{"text":"a cat"}`},
		},
		{
			name:       "content-length updated after rewrite",
			path:       "/fal/run/fal-ai/x",
			respHdrs:   map[string]string{":status": "200"},
			chunks:     []string{`{"url":"https://fal.run/fal-ai/x"}`},
			expHeaders: []internalapi.Header{{contentLengthHeaderName, "45"}},
			expBody:    []byte(`{"url":"https://gw.example/fal/run/fal-ai/x"}`),
		},
		{
			name:       "x-forwarded-proto https wins over :scheme http",
			path:       "/fal/queue/fal-ai/flux/dev",
			clientHdrs: map[string]string{":scheme": "http", "x-forwarded-proto": "https"},
			respHdrs:   map[string]string{":status": "200", "x-fal-billable-units": "0"},
			chunks:     []string{submitFal},
			expHeaders: contentLength(submitGtw),
			expBody:    []byte(submitGtw),
		},
		{
			name:       "multi-value x-forwarded-proto uses the first value",
			path:       "/fal/queue/fal-ai/flux/requests/r/status",
			clientHdrs: map[string]string{":scheme": "http", "x-forwarded-proto": "https, http"},
			respHdrs:   map[string]string{":status": "200"},
			chunks:     []string{`{"status":"COMPLETED","response_url":"https://queue.fal.run/fal-ai/flux/requests/r"}`},
			expHeaders: contentLength(statusGtw),
			expBody:    []byte(statusGtw),
		},
		{
			name:       "forged x-forwarded-proto is ignored in favour of :scheme",
			path:       "/fal/queue/fal-ai/flux/requests/r/status",
			clientHdrs: map[string]string{":scheme": "http", "x-forwarded-proto": `https","x":"`},
			respHdrs:   map[string]string{":status": "200"},
			chunks:     []string{`{"status":"COMPLETED","response_url":"https://queue.fal.run/fal-ai/flux/requests/r"}`},
			expHeaders: contentLength(strings.ReplaceAll(statusGtw, "https://", "http://")),
			expBody:    []byte(strings.ReplaceAll(statusGtw, "https://", "http://")),
		},
		{
			name:       "no valid scheme at all leaves fal's URLs alone",
			path:       "/fal/queue/fal-ai/flux/requests/r/status",
			clientHdrs: map[string]string{":scheme": "ws", "x-forwarded-proto": "ftp"},
			respHdrs:   map[string]string{":status": "200"},
			chunks:     []string{`{"status":"COMPLETED","response_url":"https://queue.fal.run/fal-ai/flux/requests/r"}`},
		},
		{
			name:       "no x-forwarded-proto uses :scheme",
			path:       "/fal/queue/fal-ai/flux/requests/r/status",
			clientHdrs: map[string]string{":scheme": "http"},
			respHdrs:   map[string]string{":status": "200"},
			chunks:     []string{`{"status":"COMPLETED","response_url":"https://queue.fal.run/fal-ai/flux/requests/r"}`},
			expHeaders: contentLength(strings.ReplaceAll(statusGtw, "https://", "http://")),
			expBody:    []byte(strings.ReplaceAll(statusGtw, "https://", "http://")),
		},
		{
			name:       "https scheme setting overrides the request scheme",
			path:       "/fal/queue/fal-ai/flux/dev",
			scheme:     internalapi.FalURLSchemeHTTPS,
			clientHdrs: map[string]string{":scheme": "http", "x-forwarded-proto": "http"},
			respHdrs:   map[string]string{":status": "200", "x-fal-billable-units": "0"},
			chunks:     []string{submitFal},
			expHeaders: contentLength(submitGtw),
			expBody:    []byte(submitGtw),
		},
		{
			name:       "http scheme setting overrides x-forwarded-proto https",
			path:       "/fal/queue/fal-ai/flux/dev",
			scheme:     internalapi.FalURLSchemeHTTP,
			clientHdrs: map[string]string{"x-forwarded-proto": "https"},
			respHdrs:   map[string]string{":status": "200", "x-fal-billable-units": "0"},
			chunks:     []string{submitFal},
			expHeaders: contentLength(strings.ReplaceAll(submitGtw, "https://", "http://")),
			expBody:    []byte(strings.ReplaceAll(submitGtw, "https://", "http://")),
		},
		{
			name:       "http scheme setting applies to error bodies",
			path:       "/fal/queue/fal-ai/flux/requests/r",
			scheme:     internalapi.FalURLSchemeHTTP,
			respHdrs:   map[string]string{":status": "400"},
			chunks:     []string{`{"detail":"Request is still in progress","status_url":"https://queue.fal.run/fal-ai/flux/requests/r/status"}`},
			expHeaders: contentLength(strings.ReplaceAll(errGtw, "https://", "http://")),
			expBody:    []byte(strings.ReplaceAll(errGtw, "https://", "http://")),
		},
		{
			name:       "error body urls point at the gateway",
			path:       "/fal/queue/fal-ai/flux/requests/r",
			respHdrs:   map[string]string{":status": "400"},
			chunks:     []string{`{"detail":"Request is still in progress","status_url":"https://queue.fal.run/fal-ai/flux/requests/r/status"}`},
			expHeaders: contentLength(errGtw),
			expBody:    []byte(errGtw),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := NewFalToFalTranslator("/fal", tt.scheme)
			tr.(RequestHeadersSetter).SetRequestHeaders(map[string]string{
				internalapi.OriginalPathHeader: tt.path,
				":authority":                   "queue.fal.run",
			})
			clientHdrs := map[string]string{
				internalapi.OriginalPathHeader: tt.path,
				":authority":                   "gw.example",
				":scheme":                      "https",
			}
			maps.Copy(clientHdrs, tt.clientHdrs)
			tr.(ClientRequestHeadersSetter).SetClientRequestHeaders(clientHdrs)
			_, _, err := tr.RequestBody(nil, &fal.Request{}, false)
			require.NoError(t, err)

			var (
				headers []internalapi.Header
				body    []byte
				usage   metrics.TokenUsage
			)
			for i, chunk := range tt.chunks {
				if tt.respHdrs[":status"] != "200" {
					h, b, err := tr.ResponseError(tt.respHdrs, bytes.NewBufferString(chunk))
					require.NoError(t, err)
					headers = append(headers, h...)
					body = append(body, b...)
					continue
				}
				h, b, u, _, err := tr.ResponseBody(tt.respHdrs, bytes.NewBufferString(chunk), i == len(tt.chunks)-1, nil)
				require.NoError(t, err)
				headers = append(headers, h...)
				body = append(body, b...)
				usage.Override(u)
			}
			require.Equal(t, tt.expHeaders, headers)
			require.Equal(t, tt.expBody, body)
			require.Equal(t, tt.expUsage, usage)
		})
	}
}

type mockFalSpan struct {
	recorded []*fal.Response
}

func (m *mockFalSpan) EndSpan()                   {}
func (m *mockFalSpan) EndSpanOnError(int, []byte) {}
func (m *mockFalSpan) RecordResponse(resp *fal.Response) {
	m.recorded = append(m.recorded, resp)
}
func (m *mockFalSpan) RecordResponseChunk(*struct{}) {}

func TestFalToFalTranslator_ResponseBody_RecordsResponseInSpan(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		model  string
		chunks []string
		exp    *fal.Response
	}{
		{
			name:   "body and usage",
			path:   "/fal/run/openrouter/router",
			model:  "openrouter/router",
			chunks: []string{`{"output":"hi","usage":{"prompt_tokens":40,"completion_tokens":227,"total_tokens":267}}`},
			exp: &fal.Response{
				Model: "openrouter/router",
				Body:  []byte(`{"output":"hi","usage":{"prompt_tokens":40,"completion_tokens":227,"total_tokens":267}}`),
				Usage: &fal.Usage{InputTokens: ptr.To(40), OutputTokens: ptr.To(227)},
			},
		},
		{
			name:   "media result without usage",
			path:   "/fal/run/fal-ai/flux/dev",
			model:  "fal-ai/flux/dev",
			chunks: []string{`{"images":[{"url":"https://v3.fal.media/files/a.png"}]}`},
			exp:    &fal.Response{Model: "fal-ai/flux/dev", Body: []byte(`{"images":[{"url":"https://v3.fal.media/files/a.png"}]}`)},
		},
		{
			name:  "stream records usage from earlier chunks once at the end",
			path:  "/fal/run/openrouter/router/stream",
			model: "openrouter/router",
			chunks: []string{
				`data: {"output":"hi","usage":{"prompt_tokens":40,"completion_tokens":227,"total_tokens":267}}` + "\n\n",
				`data: {"output":"hi","partial":false}` + "\n\n",
			},
			exp: &fal.Response{Model: "openrouter/router", Usage: &fal.Usage{InputTokens: ptr.To(40), OutputTokens: ptr.To(227)}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := NewFalToFalTranslator("/fal", internalapi.FalURLSchemeHTTPS)
			tr.(RequestHeadersSetter).SetRequestHeaders(map[string]string{
				internalapi.OriginalPathHeader:        tt.path,
				internalapi.ModelNameHeaderKeyDefault: tt.model,
			})
			_, _, err := tr.RequestBody(nil, &fal.Request{}, false)
			require.NoError(t, err)

			span := &mockFalSpan{}
			for i, chunk := range tt.chunks {
				_, _, _, _, err = tr.ResponseBody(map[string]string{":status": "200"}, bytes.NewBufferString(chunk), i == len(tt.chunks)-1, span)
				require.NoError(t, err)
			}
			require.Equal(t, []*fal.Response{tt.exp}, span.recorded)
		})
	}
}
