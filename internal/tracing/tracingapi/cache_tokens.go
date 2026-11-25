// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracingapi

import "go.opentelemetry.io/otel/trace"

// CacheTokenUsage exposes the provider-reported cache creation counts by TTL.
// The boolean distinguishes an explicit zero from an unreported count.
type CacheTokenUsage interface {
	CacheCreation5mInputTokens() (uint32, bool)
	CacheCreation1hInputTokens() (uint32, bool)
}

// CacheTokenUsageSpan optionally records cache creation counts from the
// translator, including counts absent from the translated response schema.
type CacheTokenUsageSpan interface {
	RecordCacheTokenUsage(usage CacheTokenUsage)
}

// CacheTokenUsageRecorder is implemented by conventions that expose cache TTL
// counts on spans. Other conventions leave these attributes unset.
type CacheTokenUsageRecorder interface {
	RecordCacheTokenUsage(span trace.Span, usage CacheTokenUsage)
}
