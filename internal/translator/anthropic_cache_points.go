// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import "github.com/anthropics/anthropic-sdk-go"

// addAutomaticCachePoints marks ephemeral cache_control breakpoints in an Anthropic request that has
// none: on the last tool, on the last system block and on the last content block of the last user
// turn. These follow the order Anthropic builds the cache prefix in (tools, system, messages), so
// each breakpoint caches everything before it. A position the request does not have is skipped.
//
// That is at most three breakpoints, under Anthropic's limit of four per request:
// https://platform.claude.com/docs/en/build-with-claude/prompt-caching
//
// A request that already has a breakpoint anywhere is left unchanged, so a client that places its
// own keeps full control over them.
func addAutomaticCachePoints(params *anthropic.MessageNewParams) {
	if hasAnthropicCacheControl(params) {
		return
	}
	if n := len(params.Tools); n > 0 {
		setEphemeralCacheControl(params.Tools[n-1].GetCacheControl())
	}
	if n := len(params.System); n > 0 {
		setEphemeralCacheControl(&params.System[n-1].CacheControl)
	}
	for i := len(params.Messages) - 1; i >= 0; i-- {
		msg := &params.Messages[i]
		if msg.Role != anthropic.MessageParamRoleUser {
			continue
		}
		// Tool results are sent as a user turn, so in an agent loop this is usually the last
		// tool_result block, which caches the whole conversation so far.
		if n := len(msg.Content); n > 0 {
			setEphemeralCacheControl(msg.Content[n-1].GetCacheControl())
		}
		break
	}
}

// setEphemeralCacheControl sets cc to an ephemeral breakpoint. A nil cc, returned by GetCacheControl
// for a block type that cannot carry cache_control, is ignored.
func setEphemeralCacheControl(cc *anthropic.CacheControlEphemeralParam) {
	if cc != nil {
		*cc = anthropic.NewCacheControlEphemeralParam()
	}
}

// hasAnthropicCacheControl reports whether any tool, system block, message content block or tool
// result content block of the request carries cache_control.
func hasAnthropicCacheControl(params *anthropic.MessageNewParams) bool {
	for i := range params.Tools {
		if isCacheControlSet(params.Tools[i].GetCacheControl()) {
			return true
		}
	}
	for i := range params.System {
		if isCacheControlSet(&params.System[i].CacheControl) {
			return true
		}
	}
	for i := range params.Messages {
		for j := range params.Messages[i].Content {
			block := &params.Messages[i].Content[j]
			if isCacheControlSet(block.GetCacheControl()) {
				return true
			}
			if block.OfToolResult == nil {
				continue
			}
			for _, part := range block.OfToolResult.Content {
				if isCacheControlSet(part.GetCacheControl()) {
					return true
				}
			}
		}
	}
	return false
}

// isCacheControlSet reports whether cc is a breakpoint. The translator only copies cache_control
// whose type is set (see isCacheEnabled), so an unset one has an empty type.
func isCacheControlSet(cc *anthropic.CacheControlEphemeralParam) bool {
	return cc != nil && cc.Type != ""
}
