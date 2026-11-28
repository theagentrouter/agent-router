// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

// External guardrail providers are called with selective retries and recursive bisection:
//
//   - Payload too large (filterapi.ErrGuardrailPayloadTooLarge): the payload is partitioned and
//     each half is evaluated on its own. The same payload is never retried, since waiting does
//     not fix a size error.
//   - Transient throttling (filterapi.ErrGuardrailThrottled, HTTP 429): the same payload is
//     retried up to guardrailMaxRetries more times, waiting guardrailRetryBaseDelay * 2^k
//     (0.5s, 1s, 2s) before retry k. No jitter is applied in this loop.
//   - Any other error: propagated, and handled by the guardrail's failure mode.
//   - Blocked content: Block and Monitor rules stop evaluating the remaining fragments as soon
//     as one of them matches; one match is enough to decide the outcome.
//
// Bisection rules:
//
//   - Several fragments: the list is split in two halves.
//   - A single fragment: its text is split at the whitespace closest to the midpoint.
//   - Every piece keeps the qualifiers of the field it came from (the index of the extracted
//     content, and through it its JSON path), so masked pieces are reassembled into the
//     original field.
//   - When no valid split exists (a single fragment without inner whitespace), the original
//     provider error is propagated.
//
// Limitations:
//
//   - Bisection does not overlap fragments. A harmful phrase that straddles the boundary
//     between two fragments may go undetected, because neither fragment contains it whole.
//   - The mechanism is reactive: the original payload is always tried first, and partitioning
//     only starts after the provider rejects it with a recognizable size error. A network
//     timeout, or any rejection without a recognizable size signal, does not trigger this
//     recovery.

const (
	guardrailMaxRetries     = 3
	guardrailRetryBaseDelay = 500 * time.Millisecond
)

// guardrailRetrySleep waits for d or until ctx is done. Tests replace it to avoid real waits.
var guardrailRetrySleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// errGuardrailNoMaskedContent is returned when a Mask rule matched but the provider did not
// return masked text. It is never failed open, so Mask rules do not forward unmasked content.
var errGuardrailNoMaskedContent = errors.New("returned no masked content")

// evaluateGuardrailWithRetry calls the evaluator, retrying only on transient throttling.
func evaluateGuardrailWithRetry(ctx context.Context, evaluator filterapi.GuardrailEvaluator, body []byte, phase filterapi.GuardrailPhase) (filterapi.GuardrailEvaluationResult, error) {
	for attempt := 0; ; attempt++ {
		evaluation, err := evaluator.Evaluate(ctx, body, phase)
		if err == nil || attempt >= guardrailMaxRetries || !errors.Is(err, filterapi.ErrGuardrailThrottled) {
			return evaluation, err
		}
		if sleepErr := guardrailRetrySleep(ctx, guardrailRetryBaseDelay<<attempt); sleepErr != nil {
			return evaluation, fmt.Errorf("%w (retry aborted: %w)", err, sleepErr)
		}
	}
}

// guardrailFragment is a piece of an extracted content field sent to an external provider.
type guardrailFragment struct {
	// content is the index of the extracted field the text belongs to. It carries the field's
	// qualifiers (its JSON path) across splits.
	content int
	text    []byte
}

// guardrailFragmentResult is an evaluated fragment. replacement is nil when it was not masked.
type guardrailFragmentResult struct {
	guardrailFragment
	replacement []byte
}

// guardrailBisector evaluates fragments with one external guardrail, partitioning them
// recursively when the provider rejects a payload as too large.
type guardrailBisector struct {
	ctx       context.Context
	guardrail *filterapi.RuntimeGuardrail
	phase     filterapi.GuardrailPhase
	action    filterapi.GuardrailAction

	// matched reports whether any fragment matched.
	matched bool
	// results holds the evaluated fragments in their original order; only filled for Mask rules.
	results []guardrailFragmentResult
}

func newGuardrailBisector(ctx context.Context, g *filterapi.RuntimeGuardrail, phase filterapi.GuardrailPhase) *guardrailBisector {
	return &guardrailBisector{ctx: ctx, guardrail: g, phase: phase, action: guardrailAction(g.Provider.Action)}
}

// stopped reports whether the outcome is already decided and no more fragments need evaluating.
func (b *guardrailBisector) stopped() bool {
	return b.matched && b.action != filterapi.GuardrailActionMask
}

// evaluate evaluates the fragments in a single provider call, bisecting them on size errors.
func (b *guardrailBisector) evaluate(fragments []guardrailFragment) error {
	texts := make([][]byte, len(fragments))
	for i := range fragments {
		texts[i] = fragments[i].text
	}
	evaluation, err := evaluateGuardrailWithRetry(b.ctx, b.guardrail.Evaluator, joinGuardrailTexts(texts), b.phase)
	if err != nil {
		if !errors.Is(err, filterapi.ErrGuardrailPayloadTooLarge) {
			return err
		}
		left, right, ok := bisectGuardrailFragments(fragments)
		if !ok {
			return err
		}
		if err = b.evaluate(left); err != nil || b.stopped() {
			return err
		}
		return b.evaluate(right)
	}

	if !evaluation.Matched {
		b.record(fragments, nil)
		return nil
	}
	b.matched = true
	if b.action != filterapi.GuardrailActionMask {
		return nil
	}
	if len(evaluation.Replacement) == 0 {
		return fmt.Errorf("guardrail %q matched but provider %q %w", b.guardrail.Name, b.guardrail.Provider.Type, errGuardrailNoMaskedContent)
	}
	replacements, ok := splitGuardrailReplacement(texts, evaluation.Replacement)
	if !ok {
		// The provider rewrote the text in a way that cannot be mapped back to the fragments;
		// fall back to evaluating each fragment on its own.
		for i := range fragments {
			if err = b.evaluate(fragments[i : i+1]); err != nil {
				return err
			}
		}
		return nil
	}
	b.record(fragments, replacements)
	return nil
}

func (b *guardrailBisector) record(fragments []guardrailFragment, replacements [][]byte) {
	if b.action != filterapi.GuardrailActionMask {
		return
	}
	for i := range fragments {
		result := guardrailFragmentResult{guardrailFragment: fragments[i]}
		if replacements != nil {
			result.replacement = replacements[i]
		}
		b.results = append(b.results, result)
	}
}

// maskedContents reassembles the masked text of each of the n extracted fields. Fields with no
// masked fragment are nil.
func (b *guardrailBisector) maskedContents(n int) [][]byte {
	masked := make([][]byte, n)
	changed := make([]bool, n)
	for _, result := range b.results {
		text := result.text
		if result.replacement != nil {
			text = result.replacement
			changed[result.content] = true
		}
		masked[result.content] = append(masked[result.content], text...)
	}
	for i := range masked {
		if !changed[i] {
			masked[i] = nil
		}
	}
	return masked
}

// bisectGuardrailFragments splits fragments in two non-empty halves. It returns false when no
// valid split exists.
func bisectGuardrailFragments(fragments []guardrailFragment) (left, right []guardrailFragment, ok bool) {
	switch {
	case len(fragments) > 1:
		mid := len(fragments) / 2
		return fragments[:mid], fragments[mid:], true
	case len(fragments) == 1:
		fragment := fragments[0]
		at := guardrailSplitPoint(fragment.text)
		if at < 0 {
			return nil, nil, false
		}
		return []guardrailFragment{{content: fragment.content, text: fragment.text[:at]}},
			[]guardrailFragment{{content: fragment.content, text: fragment.text[at:]}}, true
	default:
		return nil, nil, false
	}
}

// guardrailSplitPoint returns the byte offset right after the whitespace closest to the middle
// of text, so text[:at] and text[at:] are both non-empty and concatenate back to text. It
// returns -1 when text has no such whitespace.
func guardrailSplitPoint(text []byte) int {
	mid := len(text) / 2
	best, bestDistance := -1, 0
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRune(text[i:])
		if at := i + size; unicode.IsSpace(r) && at < len(text) {
			distance := i - mid
			if distance < 0 {
				distance = -distance
			}
			if best < 0 || distance < bestDistance {
				best, bestDistance = at, distance
			}
		}
		i += size
	}
	return best
}
