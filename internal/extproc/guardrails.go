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
	"slices"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

// guardrailViolation describes a trigger that should block the request/response.
type guardrailViolation struct {
	Name    string
	Message string
}

type guardrailOutcome struct {
	Violation *guardrailViolation
	Body      []byte
	Masked    bool
	Monitored bool
	RuleName  string
}

type guardrailFailOpenError struct {
	errors []error
}

func (e *guardrailFailOpenError) Error() string {
	return fmt.Sprintf("%d guardrail provider evaluation(s) failed open: %v", len(e.errors), errors.Join(e.errors...))
}

func isGuardrailFailOpenError(err error) bool {
	var failOpenError *guardrailFailOpenError
	return errors.As(err, &failOpenError)
}

func guardrailsConfiguredForPhase(guardrails []filterapi.RuntimeGuardrail, phase filterapi.GuardrailPhase, backendName string, includeGlobal bool) bool {
	for i := range guardrails {
		if guardrails[i].Phase == phase && guardrailAppliesToBackend(&guardrails[i], backendName, includeGlobal) {
			return true
		}
	}
	return false
}

func guardrailsRequireBufferedResponse(guardrails []filterapi.RuntimeGuardrail, backendName string) bool {
	for i := range guardrails {
		guardrail := &guardrails[i]
		if guardrail.Phase == filterapi.GuardrailPhaseResponse &&
			guardrailAppliesToBackend(guardrail, backendName, true) {
			return true
		}
	}
	return false
}

func evaluateGuardrailsForPhase(ctx context.Context, guardrails []filterapi.RuntimeGuardrail, phase filterapi.GuardrailPhase, body []byte, backendName string, includeGlobal bool) (guardrailOutcome, error) {
	outcome := guardrailOutcome{Body: body}
	var failOpenErrors []error
	for i := range guardrails {
		g := &guardrails[i]
		if g.Phase != phase || !guardrailAppliesToBackend(g, backendName, includeGlobal) {
			continue
		}
		maxPayloadBytes := g.MaxPayloadBytes
		if maxPayloadBytes <= 0 {
			maxPayloadBytes = 10 * 1024 * 1024
		}
		if int64(len(outcome.Body)) > maxPayloadBytes {
			err := fmt.Errorf("guardrail %q payload is %d bytes, exceeding the %d-byte limit", g.Name, len(outcome.Body), maxPayloadBytes)
			if g.Provider.FailureMode == filterapi.GuardrailFailureModeFailOpen || guardrailAction(g.Provider.Action) == filterapi.GuardrailActionMonitor {
				failOpenErrors = append(failOpenErrors, err)
				continue
			}
			return outcome, err
		}

		action := guardrailAction(g.Provider.Action)
		if g.Provider.Type == filterapi.GuardrailProviderTypeRegex && action != filterapi.GuardrailActionMask {
			if g.Matcher == nil {
				return outcome, fmt.Errorf("guardrail %q uses regex provider without a compiled matcher", g.Name)
			}
			if !g.Matcher.Match(outcome.Body) {
				continue
			}
			if action == filterapi.GuardrailActionMonitor {
				outcome.Monitored = true
				outcome.RuleName = g.Name
				continue
			}
			outcome.Violation = newGuardrailViolation(g)
			return outcome, nil
		}

		contents, err := extractGuardrailContents(outcome.Body)
		if err != nil {
			if g.Provider.FailureMode == filterapi.GuardrailFailureModeFailOpen || action == filterapi.GuardrailActionMonitor {
				failOpenErrors = append(failOpenErrors, fmt.Errorf("guardrail %q text extraction failed: %w", g.Name, err))
				continue
			}
			return outcome, fmt.Errorf("guardrail %q text extraction failed: %w", g.Name, err)
		}
		if len(contents) == 0 {
			continue
		}

		// Regex masking runs locally, so it is applied to each extracted text field directly.
		if g.Provider.Type == filterapi.GuardrailProviderTypeRegex {
			if g.Matcher == nil {
				return outcome, fmt.Errorf("guardrail %q uses regex provider without a compiled matcher", g.Name)
			}
			replacement := g.Provider.MaskReplacement
			if replacement == "" {
				replacement = "[REDACTED]"
			}
			for _, content := range contents {
				if !g.Matcher.Match(content.text) {
					continue
				}
				outcome.Body, err = replaceGuardrailContent(outcome.Body, content, g.Matcher.ReplaceAll(content.text, []byte(replacement)))
				if err != nil {
					return outcome, fmt.Errorf("guardrail %q failed to mask content: %w", g.Name, err)
				}
				outcome.Masked = true
				outcome.RuleName = g.Name
			}
			continue
		}

		// External providers receive all extracted text fields in a single call, so one LLM
		// request results in at most one request per guardrail provider.
		if g.Evaluator == nil {
			return outcome, fmt.Errorf("guardrail %q uses provider %q without an evaluator", g.Name, g.Provider.Type)
		}
		evaluation, err := g.Evaluator.Evaluate(ctx, joinGuardrailContents(contents), phase)
		if err != nil {
			if g.Provider.FailureMode == filterapi.GuardrailFailureModeFailOpen || action == filterapi.GuardrailActionMonitor {
				failOpenErrors = append(failOpenErrors, fmt.Errorf("guardrail %q evaluation failed: %w", g.Name, err))
				continue
			}
			return outcome, fmt.Errorf("guardrail %q evaluation failed: %w", g.Name, err)
		}
		if !evaluation.Matched {
			continue
		}
		switch action {
		case filterapi.GuardrailActionMonitor:
			outcome.Monitored = true
			outcome.RuleName = g.Name
		case filterapi.GuardrailActionMask:
			if len(evaluation.Replacement) == 0 {
				return outcome, fmt.Errorf("guardrail %q matched but provider %q returned no masked content", g.Name, g.Provider.Type)
			}
			replacements, ok := splitGuardrailReplacement(contents, evaluation.Replacement)
			if !ok {
				// The provider rewrote the text in a way that cannot be mapped back to the
				// original fields; fall back to evaluating each field on its own.
				replacements, err = evaluateGuardrailContentsIndividually(ctx, g, contents, phase)
				if err != nil {
					if g.Provider.FailureMode == filterapi.GuardrailFailureModeFailOpen {
						failOpenErrors = append(failOpenErrors, fmt.Errorf("guardrail %q evaluation failed: %w", g.Name, err))
						continue
					}
					return outcome, fmt.Errorf("guardrail %q evaluation failed: %w", g.Name, err)
				}
			}
			for j, content := range contents {
				if replacements[j] == nil || bytes.Equal(replacements[j], content.text) {
					continue
				}
				outcome.Body, err = replaceGuardrailContent(outcome.Body, content, replacements[j])
				if err != nil {
					return outcome, fmt.Errorf("guardrail %q failed to mask content: %w", g.Name, err)
				}
			}
			outcome.Masked = true
			outcome.RuleName = g.Name
		default:
			outcome.Violation = newGuardrailViolation(g)
			return outcome, nil
		}
	}
	if len(failOpenErrors) > 0 {
		return outcome, &guardrailFailOpenError{errors: failOpenErrors}
	}
	return outcome, nil
}

// guardrailContentSeparator joins extracted text fields into a single provider payload. It uses
// the Unicode "symbol for record separator" so it does not collide with real content and is not
// picked up by PII or content-safety detectors.
const guardrailContentSeparator = "\n␞\n"

func joinGuardrailContents(contents []guardrailContent) []byte {
	if len(contents) == 1 {
		return contents[0].text
	}
	texts := make([][]byte, len(contents))
	for i := range contents {
		texts[i] = contents[i].text
	}
	return bytes.Join(texts, []byte(guardrailContentSeparator))
}

// splitGuardrailReplacement maps a masked version of the joined payload back to the extracted
// fields. It returns false when the mapping is ambiguous, e.g. a field contains the separator
// or the provider removed or added separators.
func splitGuardrailReplacement(contents []guardrailContent, replacement []byte) ([][]byte, bool) {
	if len(contents) == 1 {
		return [][]byte{replacement}, true
	}
	separator := []byte(guardrailContentSeparator)
	for i := range contents {
		if bytes.Contains(contents[i].text, separator) {
			return nil, false
		}
	}
	parts := bytes.Split(replacement, separator)
	if len(parts) != len(contents) {
		return nil, false
	}
	return parts, true
}

// evaluateGuardrailContentsIndividually evaluates each extracted field with its own provider call.
// It returns the masked text per field, or nil for fields the provider did not match.
func evaluateGuardrailContentsIndividually(ctx context.Context, g *filterapi.RuntimeGuardrail, contents []guardrailContent, phase filterapi.GuardrailPhase) ([][]byte, error) {
	replacements := make([][]byte, len(contents))
	for i := range contents {
		evaluation, err := g.Evaluator.Evaluate(ctx, contents[i].text, phase)
		if err != nil {
			return nil, err
		}
		if !evaluation.Matched {
			continue
		}
		if len(evaluation.Replacement) == 0 {
			return nil, fmt.Errorf("provider %q matched but returned no masked content", g.Provider.Type)
		}
		replacements[i] = evaluation.Replacement
	}
	return replacements, nil
}

func guardrailAction(action filterapi.GuardrailAction) filterapi.GuardrailAction {
	if action == "" {
		return filterapi.GuardrailActionBlock
	}
	return action
}

func newGuardrailViolation(guardrail *filterapi.RuntimeGuardrail) *guardrailViolation {
	message := guardrail.Provider.Message
	if message == "" {
		message = fmt.Sprintf("request blocked by guardrail %q", guardrail.Name)
	}
	return &guardrailViolation{Name: guardrail.Name, Message: message}
}

func guardrailAppliesToBackend(guardrail *filterapi.RuntimeGuardrail, backendName string, includeGlobal bool) bool {
	if len(guardrail.Backends) == 0 {
		return includeGlobal
	}
	return backendName != "" && slices.Contains(guardrail.Backends, backendName)
}

func evaluateRequestGuardrails(ctx context.Context, guardrails []filterapi.RuntimeGuardrail, body []byte) (guardrailOutcome, error) {
	return evaluateGuardrailsForPhase(ctx, guardrails, filterapi.GuardrailPhaseRequest, body, "", true)
}

func evaluateBackendRequestGuardrails(ctx context.Context, guardrails []filterapi.RuntimeGuardrail, body []byte, backendName string) (guardrailOutcome, error) {
	return evaluateGuardrailsForPhase(ctx, guardrails, filterapi.GuardrailPhaseRequest, body, backendName, false)
}

func evaluateResponseGuardrails(ctx context.Context, guardrails []filterapi.RuntimeGuardrail, body []byte, backendName string) (guardrailOutcome, error) {
	return evaluateGuardrailsForPhase(ctx, guardrails, filterapi.GuardrailPhaseResponse, body, backendName, true)
}
