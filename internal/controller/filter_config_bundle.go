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
	// DefaultFilterConfigBundleMaxSlots is the number of bundle slots mounted in the pod when
	// Options.FilterConfigBundleMaxSlots is unset. The slot count is fixed per pod so shard count
	// changes never require remounting.
	DefaultFilterConfigBundleMaxSlots = 8
	// MaxFilterConfigBundleMaxSlots caps Options.FilterConfigBundleMaxSlots. Every slot adds a
	// projection to each gateway pod and Secret API calls to each reconcile, so an unbounded value
	// would surface as pods rejected at admission rather than as a flag error.
	MaxFilterConfigBundleMaxSlots = 128
	// filterConfigBundleTooLargeEventReason is the reason of the Warning Event recorded on the Gateway
	// when the filter config needs more bundle parts than the configured slots.
	filterConfigBundleTooLargeEventReason = "FilterConfigBundleTooLarge"
)

func splitBytes(raw []byte, chunkSize int) [][]byte {
	if len(raw) == 0 {
		return [][]byte{{}}
	}
	chunks := make([][]byte, 0, (len(raw)+chunkSize-1)/chunkSize)
	for start := 0; start < len(raw); start += chunkSize {
		end := start + chunkSize
		if end > len(raw) {
			end = len(raw)
		}
		chunks = append(chunks, raw[start:end])
	}
	return chunks
}

// filterConfigBundleTooLargeError is returned when the filter config needs more parts than the
// configured slot count. Reconcile surfaces it as a Warning Event on the Gateway, since otherwise the
// only trace of the refused write is the reconciler error log.
type filterConfigBundleTooLargeError struct {
	parts, maxSlots int
}

// Error implements error. It names the flag so the reader knows what to change.
func (e *filterConfigBundleTooLargeError) Error() string {
	return fmt.Sprintf("filter config requires %d shards, exceeds max supported slots %d; "+
		"raise --filterConfigBundleMaxSlots (Helm: controller.filterConfigBundleMaxSlots)", e.parts, e.maxSlots)
}

// filterConfigBundleIndexFromSecret parses the bundle index held by the index Secret.
func filterConfigBundleIndexFromSecret(secret *corev1.Secret) (*filterapi.ConfigBundleIndex, error) {
	raw, ok := secret.Data[FilterConfigBundleIndexKey]
	if !ok {
		// The index is written as StringData, which the API server folds into Data but the fake
		// clientset does not.
		s, ok := secret.StringData[FilterConfigBundleIndexKey]
		if !ok {
			return nil, fmt.Errorf("filter config index secret %s has no %s key", secret.Name, FilterConfigBundleIndexKey)
		}
		raw = []byte(s)
	}
	return filterapi.UnmarshalConfigBundleIndex(raw)
}

func (c *GatewayController) writeFilterConfigBundle(ctx context.Context, gatewayName, gatewayNamespace, configSecretNamespace string, payload []byte, uuid string) error {
	indexSecretName := FilterConfigBundleIndexSecretName(gatewayName, gatewayNamespace)
	chunks := splitBytes(payload, filterConfigBundlePartSizeBytes)
	maxSlots := c.filterConfigBundleMaxSlots()
	if len(chunks) > maxSlots {
		// Nothing is written past this point, so the gateway pods keep their previous config.
		return &filterConfigBundleTooLargeError{parts: len(chunks), maxSlots: maxSlots}
	}
	index := &filterapi.ConfigBundleIndex{
		Version:  version.Parse(),
		UUID:     uuid,
		Checksum: filterapi.ConfigBundleChecksum(payload),
		Parts:    make([]filterapi.ConfigBundlePart, 0, len(chunks)),
	}

	// Create parts Secrets
	for i, chunk := range chunks {
		partName := filterConfigBundlePartSecretName(gatewayName, gatewayNamespace, i)
		index.Parts = append(index.Parts, filterapi.ConfigBundlePart{
			Name:      partName,
			Path:      filterapi.ConfigBundlePartPath(i),
			SizeBytes: len(chunk),
		})
		partData := map[string][]byte{FilterConfigBundlePartKey: chunk}

		secret, err := c.kube.CoreV1().Secrets(configSecretNamespace).Get(ctx, partName, metav1.GetOptions{})
		switch {
		case err != nil && !apierrors.IsNotFound(err): // failed
			return fmt.Errorf("failed to get filter config part secret %s: %w", partName, err)
		case err != nil && apierrors.IsNotFound(err): // not found
			secret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: partName, Namespace: configSecretNamespace},
				Data:       partData,
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

	indexSecret, indexErr := c.kube.CoreV1().Secrets(configSecretNamespace).Get(ctx, indexSecretName, metav1.GetOptions{})
	if indexErr != nil && !apierrors.IsNotFound(indexErr) {
		return fmt.Errorf("failed to get filter config index secret %s: %w", indexSecretName, indexErr)
	}
	indexExists := indexErr == nil

	// Delete outdated parts from the previous configBundle. The previous index lists every part
	// written by the last successful write, including parts past maxSlots when the slot count was
	// lowered, so delete through whichever is larger. Parts are deleted before the index is
	// replaced: if a delete fails, the previous index stays and the next reconcile retries.
	stalePartsEnd := maxSlots
	if indexExists {
		if prev, prevErr := filterConfigBundleIndexFromSecret(indexSecret); prevErr == nil {
			stalePartsEnd = max(stalePartsEnd, len(prev.Parts))
		}
	}
	for i := len(chunks); i < stalePartsEnd; i++ {
		partName := filterConfigBundlePartSecretName(gatewayName, gatewayNamespace, i)
		if err := c.kube.CoreV1().Secrets(configSecretNamespace).Delete(ctx, partName, metav1.DeleteOptions{}); err != nil &&
			!apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete unused filter config part secret %s: %w", partName, err)
		}
	}

	// Create index Secret
	indexRaw, err := filterapi.MarshalConfigBundleIndex(index)
	if err != nil {
		return fmt.Errorf("failed to marshal config bundle index: %w", err)
	}
	indexStringData := map[string]string{FilterConfigBundleIndexKey: string(indexRaw)}

	if !indexExists {
		indexSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: indexSecretName, Namespace: configSecretNamespace},
			StringData: indexStringData,
		}
		if _, err = c.kube.CoreV1().Secrets(configSecretNamespace).Create(ctx, indexSecret, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("failed to create filter config index secret %s: %w", indexSecretName, err)
		}
		return nil
	}
	indexSecret.StringData = indexStringData
	if _, err = c.kube.CoreV1().Secrets(configSecretNamespace).Update(ctx, indexSecret, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update filter config index secret %s: %w", indexSecretName, err)
	}
	return nil
}
