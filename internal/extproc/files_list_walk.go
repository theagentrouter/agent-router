// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"sort"
	"strings"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/idcodec"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// backendKey identifies a route backend by its AIServiceBackend namespace/name — the same
// granularity as the selected_backend sticky-routing metadata value.
type backendKey struct {
	namespace string
	name      string
}

// listWalkCursor is the stateless position of a cross-backend Files list walk. It is carried to
// the client as an opaque, encrypted list cursor (idcodec KindListCursor) returned in last_id,
// and accepted back via the after query parameter on the next page.
//
// The walk visits the route's backends in a deterministic cycle that begins at start (the
// load-balancer's choice for the first page) and completes once it cycles back to start, so a
// single logical list is presented across all backends without any server-side state.
type listWalkCursor struct {
	// start is the backend the walk began on; the walk is complete once it returns to start.
	start backendKey
	// current is the backend this page must be served from.
	current backendKey
	// nativeAfter is the backend-native after cursor within current ("" = start of current).
	nativeAfter string
}

// listCursorPackSeparator separates the packed start key from the native after cursor inside the
// encrypted list-cursor payload. Neither a Kubernetes ns/name nor an OpenAI file id contains it.
const listCursorPackSeparator = "|"

// encodeListWalkCursor packs a walk position into an encrypted, tamper-resistant KindListCursor
// token. start (and the native after) are packed into the BackendID NativeID field; current is
// carried in Namespace/Name.
func encodeListWalkCursor(codec idcodec.Codec, c *listWalkCursor) (string, error) {
	native := c.start.namespace + "/" + c.start.name + listCursorPackSeparator + c.nativeAfter
	return codec.Encode(idcodec.BackendID{
		Kind:      idcodec.KindListCursor,
		Namespace: c.current.namespace,
		Name:      c.current.name,
		NativeID:  native,
	})
}

// decodeListWalkCursor reverses encodeListWalkCursor for an already-decoded BackendID. It returns
// false (not an error) when the id is not a well-formed list cursor, so the caller can fall back
// to treating the after parameter as a plain gateway file id.
func decodeListWalkCursor(decoded idcodec.BackendID) (listWalkCursor, bool) {
	if decoded.Kind != idcodec.KindListCursor {
		return listWalkCursor{}, false
	}
	sep := strings.Index(decoded.NativeID, listCursorPackSeparator)
	if sep == -1 {
		return listWalkCursor{}, false
	}
	startPart := decoded.NativeID[:sep]
	nativeAfter := decoded.NativeID[sep+len(listCursorPackSeparator):]
	slash := strings.Index(startPart, "/")
	if slash <= 0 || slash >= len(startPart)-1 {
		return listWalkCursor{}, false
	}
	return listWalkCursor{
		start:       backendKey{namespace: startPart[:slash], name: startPart[slash+1:]},
		current:     backendKey{namespace: decoded.Namespace, name: decoded.Name},
		nativeAfter: nativeAfter,
	}, true
}

// routeNameFromBackendName extracts the AIGatewayRoute name from a composite per-route-rule
// backend name "<ns>/<name>/route/<routeName>/rule/<i>/ref/<j>" (see
// internalapi.PerRouteRuleRefBackendName). routeName is a Kubernetes object name and therefore
// cannot contain "/", so it is exactly the segment between "/route/" and "/rule/".
func routeNameFromBackendName(composite string) (string, bool) {
	const routeMarker = "/route/"
	const ruleMarker = "/rule/"
	ri := strings.Index(composite, routeMarker)
	if ri == -1 {
		return "", false
	}
	rest := composite[ri+len(routeMarker):]
	rj := strings.Index(rest, ruleMarker)
	if rj <= 0 {
		return "", false
	}
	return rest[:rj], true
}

// orderedRouteBackends returns the deterministically-ordered, de-duplicated set of backends (by
// AIServiceBackend ns/name) that serve the given AIGatewayRoute. Ordering is by namespace then
// name so a cursor always resumes at the same position regardless of map iteration order or which
// gateway replica produced it.
func orderedRouteBackends(config *filterapi.RuntimeConfig, routeName string) []backendKey {
	if config == nil || routeName == "" {
		return nil
	}
	seen := make(map[backendKey]struct{})
	keys := make([]backendKey, 0, len(config.Backends))
	for composite := range config.Backends {
		rn, ok := routeNameFromBackendName(composite)
		if !ok || rn != routeName {
			continue
		}
		ns, name, ok := internalapi.NamespaceAndNameFromBackendName(composite)
		if !ok {
			continue
		}
		k := backendKey{namespace: ns, name: name}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].namespace != keys[j].namespace {
			return keys[i].namespace < keys[j].namespace
		}
		return keys[i].name < keys[j].name
	})
	return keys
}

// indexOfBackend returns the position of k in ordered, or -1 when absent.
func indexOfBackend(ordered []backendKey, k backendKey) int {
	for i := range ordered {
		if ordered[i] == k {
			return i
		}
	}
	return -1
}

// nextWalkStep decides whether the walk continues after serving a page from current, and if so
// the next cursor position. upstreamHasMore/lastNativeID describe the page just served by
// current; ordered is the route's stable backend cycle.
//
//   - If current still has more files, the walk continues within current (native after cursor).
//   - Otherwise it advances to the next backend in the deterministic cycle, completing once it
//     returns to start.
//   - If start was removed mid-walk (churn) the cycle falls back to a single linear pass ending
//     at the tail of the ordered list (eventual-consistency; see the fan-out plan RISK-003).
func nextWalkStep(ordered []backendKey, start, current backendKey, lastNativeID string, upstreamHasMore bool) (hasMore bool, next listWalkCursor) {
	if upstreamHasMore && lastNativeID != "" {
		return true, listWalkCursor{start: start, current: current, nativeAfter: lastNativeID}
	}
	ci := indexOfBackend(ordered, current)
	if ci == -1 || len(ordered) == 0 {
		// current is not part of the route's backend set (or there is no set): terminate rather
		// than risk an incorrect or unbounded walk.
		return false, listWalkCursor{}
	}
	nextIdx := (ci + 1) % len(ordered)
	startIdx := indexOfBackend(ordered, start)
	if nextIdx == startIdx || (startIdx == -1 && nextIdx == 0) {
		return false, listWalkCursor{}
	}
	return true, listWalkCursor{start: start, current: ordered[nextIdx], nativeAfter: ""}
}
