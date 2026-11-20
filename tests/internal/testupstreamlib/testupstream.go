// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package testupstreamlib

const (
	// ResponseTypeKey is the key for the response type in the request.
	// This can be either empty, "sse", or "aws-event-stream".
	//	* If this is "sse", the response body is expected to be a Server-Sent Event stream.
	// 	Each line in x-response-body is treated as a separate [data] payload.
	//	* If this is "aws-event-stream", the response body is expected to be an AWS Event Stream.
	// 	Each line in x-response-body is treated as a separate event payload.
	//	* If this is gzip, the response body is expected to be a regular JSON response compressed with gzip.
	//	* If this is empty, the response body is expected to be a regular JSON response.
	ResponseTypeKey = "x-response-type"
	// ExpectedHeadersKey is the key for the expected headers in the request.
	// The value is a base64 encoded string of comma separated key-value pairs.
	// E.g. "key1:value1,key2:value2".
	ExpectedHeadersKey = "x-expected-headers"
	// ExpectedPathHeaderKey is the key for the expected path in the request.
	// The value is a base64 encoded.
	ExpectedPathHeaderKey = "x-expected-path"
	// ExpectedRawQueryHeaderKey is the key for the expected raw query in the request.
	// E.g. "key1=value1&key2=value2".
	ExpectedRawQueryHeaderKey = "x-expected-raw-query"
	// ExpectedNoQueryHeaderKey, when set to any value, requires the request to carry no query string.
	ExpectedNoQueryHeaderKey = "x-expected-no-query"
	// ExpectedMethodHeaderKey is the key for the expected HTTP method of the request, e.g. "GET".
	ExpectedMethodHeaderKey = "x-expected-method"
	// ExpectedRequestBodyHeaderKey is the key for the expected request body in the request.
	// The value is a base64 encoded.
	ExpectedRequestBodyHeaderKey = "x-expected-request-body"
	// ResponseStatusKey is the key for the response status in the response, default is 200 if not set.
	ResponseStatusKey = "x-response-status"
	// ResponseHeadersKey is the key for the response headers in the response.
	// The value is a base64 encoded string of comma separated key-value pairs.
	// E.g. "key1:value1,key2:value2".
	ResponseHeadersKey = "x-response-headers"
	// ResponseBodyHeaderKey is the key for the response body in the response.
	// The value is a base64 encoded.
	ResponseBodyHeaderKey = "x-response-body"
	// NonExpectedRequestHeadersKey is the key for the non-expected request headers.
	// The value is a base64 encoded string of comma separated header keys expected to be absent.
	NonExpectedRequestHeadersKey = "x-non-expected-request-headers"
	// ExpectedTestUpstreamIDKey is the key for the expected testupstream-id in the request,
	// and the value will be compared with the TESTUPSTREAM_ID environment variable.
	// If the values do not match, the request will be rejected, meaning that the request
	// was routed to the wrong upstream.
	ExpectedTestUpstreamIDKey = "x-expected-testupstream-id"
	// ExpectedHostKey is the key for the expected host in the request.
	ExpectedHostKey = "x-expected-host"
	// FakeResponseHeaderKey is the key to trigger a fake response body.
	//
	// The value must be either "small", "medium" or "large".
	FakeResponseHeaderKey = "x-fake-response"
)
