// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package fal contains the request types and path parsing for fal's run and queue APIs.
//
// https://docs.fal.ai/model-apis
package fal

import (
	"regexp"
	"slices"
	"strings"
)

const (
	// MaxEndpointIDLength bounds a fal endpoint ID (owner/app[/path]) so an oversized one fails here, not in Envoy.
	MaxEndpointIDLength = 256
	// MaxRequestIDLength bounds a queue request ID; fal issues UUIDs.
	MaxRequestIDLength = 128
)

// apiSegmentRegexp is the allowed shape of one segment of a fal endpoint ID or request ID.
var apiSegmentRegexp = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Mode is the fal API a request is for.
type Mode string

const (
	// ModeRun is the synchronous API served at fal.run.
	ModeRun Mode = "run"
	// ModeQueue is the asynchronous API served at queue.fal.run.
	ModeQueue Mode = "queue"
)

var modes = []Mode{ModeRun, ModeQueue}

// Request is a fal request body. Bodies are forwarded unchanged and never decoded.
type Request struct{}

// Response is the part of a fal response that is recorded on trace spans.
type Response struct {
	// Model is the fal endpoint ID the request was routed by.
	Model string
	// Body is the upstream response body. It is nil for streamed responses.
	Body []byte
	// Usage is set when the response reports token usage, as LLM endpoints such as openrouter/router do.
	Usage *Usage
}

// Usage is the token usage of a fal response.
type Usage struct {
	InputTokens  *int
	OutputTokens *int
}

// Path is a parsed gateway path of the form <root>/<prefix>/{run,queue}/<endpoint ID>[...].
type Path struct {
	Mode Mode
	// Model is the fal endpoint ID, or owner/app for queue requests that poll by request ID.
	Model string
	// Poll is true for queue status, result and cancel requests.
	Poll bool
	// Billed is true for the legs fal bills and reports x-fal-billable-units on: run and the queue result.
	Billed bool
	// Stream is true when fal answers with server-sent events.
	Stream bool
	// Upstream is the path, with the query, to send to fal.
	Upstream string
}

// ParsePath parses a gateway fal path served under prefix. It returns false when the path names no fal endpoint.
func ParsePath(prefix string, p string) (Path, bool) {
	var query string
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p, query = p[:i], p[i:]
	}
	mode, rest, ok := cutMode(prefix, p)
	if !ok {
		return Path{}, false
	}
	segments := strings.Split(rest, "/")
	out := Path{Mode: mode, Upstream: "/" + rest + query}
	if i := slices.IndexFunc(segments, isRequestsSegment); mode == ModeQueue && i >= 0 {
		if !validEndpointID(segments[:i]) || !validQueueTail(segments[i+1:]) {
			return Path{}, false
		}
		out.Poll = true
		out.Billed = len(segments) == i+2
		out.Model = strings.Join(segments[:i], "/")
		out.Stream = strings.HasSuffix(rest, "/status/stream")
		return out, true
	}
	if mode == ModeRun && segments[len(segments)-1] == "stream" {
		out.Stream = true
		segments = segments[:len(segments)-1]
	}
	if !validEndpointID(segments) {
		return Path{}, false
	}
	out.Billed = mode == ModeRun
	out.Model = strings.Join(segments, "/")
	return out, true
}

// validEndpointID reports whether segments form a fal endpoint ID: owner/app[/path...], each segment in
// apiSegmentRegexp, within MaxEndpointIDLength, and never the queue's "requests" namespace.
func validEndpointID(segments []string) bool {
	if len(segments) < 2 || len(strings.Join(segments, "/")) > MaxEndpointIDLength {
		return false
	}
	return !slices.ContainsFunc(segments, func(s string) bool { return isRequestsSegment(s) || !apiSegmentRegexp.MatchString(s) })
}

// isRequestsSegment reports whether s names fal's queue namespace, in any letter case.
func isRequestsSegment(s string) bool {
	return strings.EqualFold(s, "requests")
}

// validQueueTail reports whether tail is one of fal's request paths: {id}, {id}/status, {id}/status/stream or {id}/cancel.
func validQueueTail(tail []string) bool {
	if len(tail) == 0 || len(tail[0]) > MaxRequestIDLength || !apiSegmentRegexp.MatchString(tail[0]) {
		return false
	}
	switch rest := tail[1:]; {
	case len(rest) == 0:
		return true
	case len(rest) == 1:
		return rest[0] == "status" || rest[0] == "cancel"
	case len(rest) == 2:
		return rest[0] == "status" && rest[1] == "stream"
	}
	return false
}

// cutMode finds the first <prefix>/run/ or <prefix>/queue/ in p and returns its mode and what follows it.
func cutMode(prefix, p string) (Mode, string, bool) {
	var (
		mode Mode
		at   = -1
	)
	for _, m := range modes {
		if i := strings.Index(p, modeSegment(prefix, m)); i >= 0 && (at < 0 || i < at) {
			mode, at = m, i
		}
	}
	if at < 0 {
		return "", "", false
	}
	return mode, p[at+len(modeSegment(prefix, mode)):], true
}

// modeSegment returns <prefix>/<mode>/ with prefix cleaned to a single leading slash, or /<mode>/ for a root prefix.
func modeSegment(prefix string, m Mode) string {
	if p := strings.Trim(prefix, "/"); p != "" {
		return "/" + p + "/" + string(m) + "/"
	}
	return "/" + string(m) + "/"
}
