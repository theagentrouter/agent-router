// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/version"
)

const (
	FilterConfigBundleIndexKey = filterapi.ConfigBundleIndexFileName
	FilterConfigBundlePartKey  = "chunk"

	// Keep each part comfortably below Kubernetes object size limits.
	filterConfigBundlePartSizeBytes = 700 * 1024
	// Fixed number of bundle slots mounted in the pod so shard count changes never require remounting.
	// We can make this configurable in the future if needed.
	maxFilterConfigBundleSlots = 8
)

func splitBytes(raw []byte, chunkSize int) [][]byte {
	if len(raw) == 0 {
		return [][]byte{{}}
	}
	chunks := make([][]byte, 0, (len(raw)+chunkSize-1)/chunkSize)
	for start := 0; start < len(raw); start += chunkSize {
		end := min(start+chunkSize, len(raw))
		chunks = append(chunks, raw[start:end])
	}
	return chunks
}

func (c *GatewayController) writeFilterConfigBundle(ctx context.Context, gatewayName, gatewayNamespace, configSecretNamespace string, payload []byte, uuid string) error {
	indexSecretName := FilterConfigBundleIndexSecretName(gatewayName, gatewayNamespace)
	chunks := splitBytes(payload, filterConfigBundlePartSizeBytes)
	if len(chunks) > maxFilterConfigBundleSlots {
		return fmt.Errorf("filter config requires %d shards, exceeds max supported slots %d", len(chunks), maxFilterConfigBundleSlots)
	}
	index := &filterapi.ConfigBundleIndex{
		Version:  version.Parse(),
		UUID:     uuid,
		Checksum: filterapi.ConfigBundleChecksum(payload),
		Parts:    make([]filterapi.ConfigBundlePart, 0, len(chunks)),
	}

	// Create parts Secrets
	for i := range maxFilterConfigBundleSlots {
		partName := filterConfigBundlePartSecretName(gatewayName, gatewayNamespace, i)

		// Delete outdated parts from the previous configBundle
		if i >= len(chunks) {
			if err := c.kube.CoreV1().Secrets(configSecretNamespace).Delete(ctx, partName, metav1.DeleteOptions{}); err != nil &&
				!apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete unused filter config part secret %s: %w", partName, err)
			}
			continue
		}

		index.Parts = append(index.Parts, filterapi.ConfigBundlePart{
			Name:      partName,
			Path:      filterapi.ConfigBundlePartPath(i),
			SizeBytes: len(chunks[i]),
		})
		partData := map[string][]byte{FilterConfigBundlePartKey: chunks[i]}

		secret, err := c.kube.CoreV1().Secrets(configSecretNamespace).Get(ctx, partName, metav1.GetOptions{})
		switch {
		case err != nil && !apierrors.IsNotFound(err): // failed
			return fmt.Errorf("failed to get filter config part secret %s: %w", partName, err)
		case err != nil && apierrors.IsNotFound(err): // not found
			secret = &corev1.Secret{
				Name: partName, Namespace: configSecretNamespace,
				Data: partData,
			}
			if _, err = c.kube.CoreV1().Secrets(configSecretNamespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
				return fmt.Errorf("failed to create filter config part secret %s: %w", partName, err)
			}
		case err == nil: // found
			secret.Data = partData
			if _, err = c.kube.CoreV1().Secrets(configSecretNamespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("failed to update filter config part secret %s: %w", partName, err)
			}
		}
	}

	// Create index Secret
	indexRaw, err := filterapi.MarshalConfigBundleIndex(index)
	if err != nil {
		return fmt.Errorf("failed to marshal config bundle index: %w", err)
	}
	indexStringData := map[string]string{FilterConfigBundleIndexKey: string(indexRaw)}

	indexSecret, err := c.kube.CoreV1().Secrets(configSecretNamespace).Get(ctx, indexSecretName, metav1.GetOptions{})
	switch {
	case err != nil && !apierrors.IsNotFound(err): // failed
		return fmt.Errorf("failed to get filter config index secret %s: %w", indexSecretName, err)
	case err != nil && apierrors.IsNotFound(err): // not found
		indexSecret = &corev1.Secret{
			Name: indexSecretName, Namespace: configSecretNamespace,
			StringData: indexStringData,
		}
		if _, err = c.kube.CoreV1().Secrets(configSecretNamespace).Create(ctx, indexSecret, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("failed to create filter config index secret %s: %w", indexSecretName, err)
		}
	case err == nil: // found
		indexSecret.StringData = indexStringData
		if _, err = c.kube.CoreV1().Secrets(configSecretNamespace).Update(ctx, indexSecret, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("failed to update filter config index secret %s: %w", indexSecretName, err)
		}
	}
	return nil
}
