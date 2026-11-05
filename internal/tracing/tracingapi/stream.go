// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracingapi

import "go.opentelemetry.io/otel/trace"

// StreamRecorderFactory optionally replaces retention of all response chunks
// with request-local recording state. Return nil to use the existing batch path.
type StreamRecorderFactory[ChunkT any] interface {
	NewStreamRecorder() StreamRecorder[ChunkT]
}

// StreamRecorder consumes borrowed chunks without modifying them or retaining
// their content. RecordAttributes must not end the span or set its status.
type StreamRecorder[ChunkT any] interface {
	RecordChunk(*ChunkT)
	RecordAttributes(trace.Span)
}
