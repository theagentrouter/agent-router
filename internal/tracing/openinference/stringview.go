// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openinference

import "unsafe"

// StringView returns a string that shares b's underlying array. Callers must
// not mutate b while any derived string remains reachable (for example while a
// span attribute still holds input.value). Prefer this over string(b) when
// recording large request bodies into span attributes to avoid a full copy.
func StringView(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}
