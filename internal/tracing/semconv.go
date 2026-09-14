// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracing

import (
	"fmt"
	"os"
	"strings"

	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference/cohere"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference/openai"
	"github.com/envoyproxy/ai-gateway/internal/tracing/otelgenai"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// EnvTracingSemConv selects the semantic convention used to record span
// attributes. Unset selects the first entry in semConvs, preserving the
// behavior from before this variable existed.
const EnvTracingSemConv = "AI_GATEWAY_TRACING_SEMCONV"

// recorderSet bundles every span recorder for one semantic convention. Adding
// an endpoint adds a field here, and TestSemConvs_complete then fails until
// every convention supplies it: keyed struct literals still compile with a
// field missing, so the test is what stops a convention shipping a nil
// recorder.
type recorderSet struct {
	chatCompletion  tracingapi.ChatCompletionRecorder
	completion      tracingapi.CompletionRecorder
	embeddings      tracingapi.EmbeddingsRecorder
	imageGeneration tracingapi.ImageGenerationRecorder
	responses       tracingapi.ResponsesRecorder
	speech          tracingapi.SpeechRecorder
	transcription   tracingapi.TranscriptionRecorder
	translation     tracingapi.TranslationRecorder
	rerank          tracingapi.RerankRecorder
	message         tracingapi.MessageRecorder
	tokenize        tracingapi.TokenizeRecorder

	responsesInputTokens tracingapi.ResponsesInputTokensRecorder
	countTokens          tracingapi.CountTokensRecorder

	// mcp is the vocabulary used for MCP spans. MCP is not an LLM endpoint and
	// has no recorder, but it is convention-scoped for the same reason the
	// endpoints are: the attribute keys and span names a user's dashboards query
	// must not change unless the user asks for it.
	mcp *mcpVocabulary

	// unboundedAttributeCount reports whether this convention emits indexed
	// per-message attributes. Those scale with conversation length and exceed
	// OTEL's default cap of 128, silently truncating spans. Only conventions
	// that need it lift the cap, so the others retain OTEL defaults.
	unboundedAttributeCount bool
}

// semConv couples a convention's name to its recorder constructor so the two
// cannot drift.
type semConv struct {
	name         string
	newRecorders func() recorderSet
}

// semConvs lists the supported semantic conventions. The first entry is the
// default used when EnvTracingSemConv is unset.
var semConvs = []semConv{
	{name: "openinference", newRecorders: newOpenInferenceRecorders},
	{name: "gen_ai", newRecorders: newOTelGenAIRecorders},
}

// newRecordersFromEnv resolves EnvTracingSemConv to a recorderSet.
//
// An unrecognized value is an error rather than a silent fallback: a gateway
// that runs for months emitting a convention nobody is watching, or whose
// redaction settings are read by a config object nobody consults, is a worse
// failure than refusing to start.
func newRecordersFromEnv() (recorderSet, error) {
	name := os.Getenv(EnvTracingSemConv)
	if name == "" {
		return semConvs[0].newRecorders(), nil
	}
	for _, sc := range semConvs {
		if sc.name == name {
			return sc.newRecorders(), nil
		}
	}
	names := make([]string, len(semConvs))
	for i, sc := range semConvs {
		names[i] = sc.name
	}
	return recorderSet{}, fmt.Errorf("invalid %s %q: must be one of %s",
		EnvTracingSemConv, name, strings.Join(names, ", "))
}

// newOpenInferenceRecorders builds the OpenInference recorders.
//
// The config is read once and shared by every recorder. This is equivalent to
// each recorder calling openinference.NewTraceConfigFromEnv() itself, but makes
// config drift between endpoints structurally impossible.
func newOpenInferenceRecorders() recorderSet {
	cfg := openinference.NewTraceConfigFromEnv()
	return recorderSet{
		chatCompletion:  openai.NewChatCompletionRecorder(cfg),
		completion:      openai.NewCompletionRecorder(cfg),
		embeddings:      openai.NewEmbeddingsRecorder(cfg),
		imageGeneration: openai.NewImageGenerationRecorder(cfg),
		responses:       openai.NewResponsesRecorder(cfg),
		speech:          openai.NewSpeechRecorder(cfg),
		transcription:   openai.NewTranscriptionRecorder(cfg),
		translation:     openai.NewTranslationRecorder(cfg),
		rerank:          cohere.NewRerankRecorder(cfg),
		message:         anthropic.NewMessageRecorder(cfg),
		tokenize:        openai.NewTokenizeRecorder(cfg),

		responsesInputTokens: openai.NewResponsesInputTokensRecorder(cfg),
		countTokens:          anthropic.NewCountTokensRecorder(cfg),

		// OpenInference defines no MCP conventions, so MCP spans keep the
		// gateway-specific vocabulary they have always emitted.
		mcp: mcpVocabularyLegacy(),

		// OpenInference emits llm.input_messages.N.* per message.
		unboundedAttributeCount: cfg.CapturesMessages(),
	}
}

// newOTelGenAIRecorders builds the OpenTelemetry GenAI recorders.
func newOTelGenAIRecorders() recorderSet {
	cfg := otelgenai.NewConfigFromEnv()
	return recorderSet{
		chatCompletion:  otelgenai.NewChatCompletionRecorder(cfg),
		completion:      otelgenai.NewCompletionRecorder(cfg),
		embeddings:      otelgenai.NewEmbeddingsRecorder(cfg),
		imageGeneration: otelgenai.NewImageGenerationRecorder(cfg),
		responses:       otelgenai.NewResponsesRecorder(cfg),
		speech:          otelgenai.NewSpeechRecorder(cfg),
		transcription:   otelgenai.NewTranscriptionRecorder(cfg),
		translation:     otelgenai.NewTranslationRecorder(cfg),
		rerank:          otelgenai.NewRerankRecorder(cfg),
		message:         otelgenai.NewMessageRecorder(cfg),
		tokenize:        otelgenai.NewTokenizeRecorder(cfg),

		responsesInputTokens: otelgenai.NewResponsesInputTokensRecorder(cfg),
		countTokens:          otelgenai.NewCountTokensRecorder(cfg),

		mcp: mcpVocabularyOTel(cfg.CaptureMessageContent),

		// GenAI puts message content in a single JSON attribute per direction
		// rather than one attribute per message, so the default cap is enough.
		unboundedAttributeCount: false,
	}
}
