// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"net/url"
	"strconv"
	"strings"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

// This file holds the helpers shared by the processors that route by a gateway-encoded id
// (Files API and Batch API): single-backend route-rule checks, path/query rewriting, and
// processing-response builders.

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

// passThroughResponseBody returns a response-body processing result that leaves the body
// unmodified.
func passThroughResponseBody() *extprocv3.ProcessingResponse {
	return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseBody{}}
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
// backend-native id before a list request is forwarded upstream.
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

// isSuccessStatus reports whether the captured upstream response status is 2xx. When the status
// is unknown (absent or unparsable), the response is treated as successful so the re-encode path
// still guards against leaking backend-native ids.
func isSuccessStatus(responseHeaders map[string]string) bool {
	status, ok := responseHeaders[":status"]
	if !ok {
		return true
	}
	code, err := strconv.Atoi(status)
	if err != nil {
		return true
	}
	return code >= 200 && code < 300
}
