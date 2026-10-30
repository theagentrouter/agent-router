// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package fal

import (
	"strings"
)

const (
	runHost   = "https://fal.run/"
	queueHost = "https://queue.fal.run/"
)

// ValidScheme returns scheme lowercased when it is http or https, else "".
func ValidScheme(scheme string) string {
	switch s := strings.ToLower(scheme); s {
	case "http", "https":
		return s
	}
	return ""
}

// GatewayBases returns the gateway URLs that stand in for https://fal.run/ and https://queue.fal.run/,
// keeping any root prefix the client path has before the fal prefix. authority is the client's :authority
// and must be a host with an optional port, scheme must be http or https; anything else (userinfo, a path,
// quoting) gives ok=false and the URLs are left as fal sent them.
func GatewayBases(prefix, originalPath, authority, scheme string) (run, queue string, ok bool) {
	if !validAuthority(authority) || ValidScheme(scheme) == "" {
		return "", "", false
	}
	if i := strings.IndexByte(originalPath, '?'); i >= 0 {
		originalPath = originalPath[:i]
	}
	mode, rest, ok := cutMode(prefix, originalPath)
	if !ok {
		return "", "", false
	}
	root := scheme + "://" + authority + originalPath[:len(originalPath)-len(rest)-len(modeSegment(prefix, mode))]
	return root + modeSegment(prefix, ModeRun), root + modeSegment(prefix, ModeQueue), true
}

// validAuthority reports whether authority is host[:port]: a DNS name or IP literal plus an optional
// numeric port, with no userinfo, path or characters that would need escaping inside a JSON string.
func validAuthority(authority string) bool {
	if authority == "" || len(authority) > 255 {
		return false
	}
	host, port := authority, ""
	if strings.HasPrefix(authority, "[") {
		end := strings.IndexByte(authority, ']')
		if end < 0 || !isIPv6Literal(authority[1:end]) {
			return false
		}
		host, port = authority[:end+1], authority[end+1:]
	} else if i := strings.LastIndexByte(authority, ':'); i >= 0 {
		host, port = authority[:i], authority[i:]
	}
	if host == "" || (!strings.HasPrefix(host, "[") && !isHostName(host)) {
		return false
	}
	if port == "" {
		return true
	}
	if port[0] != ':' || len(port) < 2 || len(port) > 6 {
		return false
	}
	for _, c := range port[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isHostName(host string) bool {
	return !strings.ContainsFunc(host, func(c rune) bool { return !isHostNameByte(c) })
}

func isHostNameByte(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_'
}

func isIPv6Literal(host string) bool {
	return host != "" && !strings.ContainsFunc(host, func(c rune) bool { return !isIPv6Byte(c) })
}

func isIPv6Byte(c rune) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' || c == ':' || c == '.'
}

// RewriteURLs replaces the fal.run and queue.fal.run hosts in body with the gateway bases, keeping fal's path and query.
func RewriteURLs(body []byte, run, queue string) []byte {
	if !strings.Contains(string(body), "fal.run/") {
		return body
	}
	return []byte(strings.NewReplacer(queueHost, queue, runHost, run).Replace(string(body)))
}

// SSERewriter applies RewriteURLs to a chunked stream, holding back a chunk tail that may begin a fal URL.
type SSERewriter struct {
	run, queue string
	carry      []byte
}

// NewSSERewriter returns an SSERewriter for the given gateway bases.
func NewSSERewriter(run, queue string) *SSERewriter {
	return &SSERewriter{run: run, queue: queue}
}

// Write returns the rewritten part of the stream that can be sent; eos flushes everything held back.
func (r *SSERewriter) Write(chunk []byte, eos bool) []byte {
	data := append(append([]byte(nil), r.carry...), chunk...)
	split := len(data)
	if !eos {
		split -= partialHostSuffix(data)
	}
	r.carry = append([]byte(nil), data[split:]...)
	return RewriteURLs(data[:split], r.run, r.queue)
}

// partialHostSuffix returns the length of the longest suffix of data that is a proper prefix of a fal host.
func partialHostSuffix(data []byte) int {
	for n := min(len(data), len(queueHost)-1); n > 0; n-- {
		tail := string(data[len(data)-n:])
		if strings.HasPrefix(queueHost, tail) || strings.HasPrefix(runHost, tail) {
			return n
		}
	}
	return 0
}
