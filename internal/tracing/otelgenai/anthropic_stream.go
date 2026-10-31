// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"strings"

	"go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

type anthropicMetadataRecorder struct {
	*recorder[anthropic.MessagesRequest, anthropic.MessagesResponse, anthropic.MessagesStreamChunk]
}

func (*anthropicMetadataRecorder) NewStreamRecorder() tracingapi.StreamRecorder[anthropic.MessagesStreamChunk] {
	return &anthropicStreamMetadata{}
}

type anthropicStreamMetadata struct {
	response anthropic.MessagesResponse
}

func (s *anthropicStreamMetadata) RecordChunk(chunk *anthropic.MessagesStreamChunk) {
	if chunk == nil {
		return
	}
	switch {
	case chunk.MessageStart != nil:
		start := chunk.MessageStart
		s.response = anthropic.MessagesResponse{
			ID: strings.Clone(start.ID), Model: strings.Clone(start.Model),
		}
		if start.Usage != nil {
			usage := *start.Usage
			s.response.Usage = &usage
		}
		if start.StopReason != nil {
			reason := anthropic.StopReason(strings.Clone(string(*start.StopReason)))
			s.response.StopReason = &reason
		}
	case chunk.MessageDelta != nil:
		delta := chunk.MessageDelta
		if s.response.Usage == nil {
			usage := delta.Usage
			s.response.Usage = &usage
		} else {
			s.response.Usage.OutputTokens = delta.Usage.OutputTokens
		}
		reason := anthropic.StopReason(strings.Clone(string(delta.Delta.StopReason)))
		s.response.StopReason = &reason
	}
}

func (s *anthropicStreamMetadata) RecordAttributes(span trace.Span) {
	span.SetAttributes(anthropicResponseAttrs(&s.response)...)
}
