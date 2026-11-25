// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	k8sObjectNameMaxLen = 253
	k8sVolumeNameMaxLen = 63
)

func shortStableHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	// 12 hex chars is enough to avoid practical collisions for this scope.
	return hex.EncodeToString(sum[:6])
}

// truncate base to fit maxLen and append hash, hash is included in maxLen
func truncateAndAppendHash(base, hash string, maxLen int) string {
	base = strings.Trim(base, "-")
	name := fmt.Sprintf("%s-%s", base, hash)
	if len(name) <= maxLen {
		return name
	}
	allowedBaseLen := maxLen - 1 - len(hash) // "<base>-<hash>"
	if allowedBaseLen <= 0 {
		return hash
	}
	if allowedBaseLen > len(base) {
		allowedBaseLen = len(base)
	}
	trimmedBase := strings.Trim(base[:allowedBaseLen], "-")
	if trimmedBase == "" {
		return hash
	}
	return fmt.Sprintf("%s-%s", trimmedBase, hash)
}

// example: gateway-ns1-c6d39be275c7
func FilterConfigBundleIndexSecretName(gwName, gwNamespace string) string {
	rawIdentity := fmt.Sprintf("%s/%s", gwNamespace, gwName)
	return truncateAndAppendHash(fmt.Sprintf("%s-%s", gwName, gwNamespace), shortStableHash(rawIdentity), k8sObjectNameMaxLen)
}

// example: gateway-ns1-c6d39be275c7-part-000
func filterConfigBundlePartSecretName(gwName, gwNamespace string, idx int) string {
	rawIdentity := fmt.Sprintf("%s/%s", gwNamespace, gwName)
	hash := shortStableHash(rawIdentity)
	base := fmt.Sprintf("%s-%s", gwName, gwNamespace)
	suffix := fmt.Sprintf("-part-%03d", idx)
	maxNameLen := k8sObjectNameMaxLen - len(hash) - len(suffix) - 1

	if len(base) > maxNameLen {
		base = strings.TrimRight(base[:maxNameLen], "-")
	}
	return base + "-" + hash + suffix
}

// example: ai-gateway-gateway-ns1-c6d39be275c7-bundle
func filterConfigBundleVolumeName(gwName, gwNamespace string) string {
	const suffix = "-bundle"
	rawIdentity := fmt.Sprintf("%s/%s", gwNamespace, gwName)
	volumeBase := fmt.Sprintf("%s%s-%s", mutationNamePrefix, gwName, gwNamespace)
	base := truncateAndAppendHash(volumeBase, shortStableHash(rawIdentity), k8sVolumeNameMaxLen-len(suffix))
	return base + suffix
}
