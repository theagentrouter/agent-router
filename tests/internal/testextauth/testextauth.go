// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package testextauth

const (
	// ExtAuthAccessControlHeader is the header used to send the access control value to
	// configure the response that will be returned by the ext-authz filter.
	ExtAuthAccessControlHeader = "x-access-control"

	// ExtAuthAllowedValueEnvVar is the name of the environment variable that will configure
	// the allowed value for the access control header. If not set, all requests are allowed.
	ExtAuthAllowedValueEnvVar = "EXT_AUTH_ALLOWED_VALUE"

	// ExtAuthDynamicMetadataHeaderEnvVar names the request header whose value picks which dynamic
	// metadata the server returns on an allowed response. ext_authz exposes that metadata under the
	// envoy.filters.http.ext_authz namespace.
	ExtAuthDynamicMetadataHeaderEnvVar = "EXT_AUTH_DYNAMIC_METADATA_HEADER"

	// ExtAuthDynamicMetadataByHeaderEnvVar is a JSON object mapping a value of that header to the
	// fields to emit. Values are arbitrary JSON, so a field can be the struct Envoy's rate limit
	// override reads:
	//
	//	{"premium": {"total_limit": {"requests_per_unit": 6, "unit": "HOUR"}}}
	//
	// A header value with no entry gets no metadata at all, which is how a test covers the "source
	// said nothing about this request" case. Nothing is emitted unless both env vars are set.
	ExtAuthDynamicMetadataByHeaderEnvVar = "EXT_AUTH_DYNAMIC_METADATA_BY_HEADER"
)
