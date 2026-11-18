// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import "strings"

// isKServePredictivePath reports whether path is a KServe V1 or V2 (Open
// Inference Protocol) inference operation, optionally behind a resource
// prefix such as /<endpoint-id>. Those requests are routed by plain
// HTTPRoutes on the same Gateway and carry no model field, so the router
// filter must pass them through instead of rejecting an unsupported path.
func isKServePredictivePath(path string) bool {
	if i := strings.Index(path, "/v1/models/"); i >= 0 {
		rest := path[i+len("/v1/models/"):]
		name, verb, ok := strings.Cut(rest, ":")
		return ok && name != "" && !strings.Contains(name, "/") && (verb == "predict" || verb == "explain")
	}
	if i := strings.Index(path, "/v2/models/"); i >= 0 {
		rest := path[i+len("/v2/models/"):]
		segments := strings.Split(rest, "/")
		switch {
		case len(segments) == 2:
			return segments[0] != "" && segments[1] == "infer"
		case len(segments) == 4:
			return segments[0] != "" && segments[1] == "versions" && segments[2] != "" && segments[3] == "infer"
		}
	}
	return false
}
