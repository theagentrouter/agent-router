// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_proc/v3"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/types/known/structpb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	gwaiev1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"

	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// TestPortForInferencePool_EdgeCases covers edge cases for port selection
// in portForInferencePool, such as invalid or out-of-range ports.
func TestPortForInferencePool_EdgeCases(t *testing.T) {
	pool := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-pool",
			Namespace: "my-ns",
		},
		Spec: gwaiev1.InferencePoolSpec{
			EndpointPickerRef: &gwaiev1.EndpointPickerRef{
				Name: "my-picker",
				Port: ptr.To(gwaiev1.Port{Number: 8080}),
			},
		},
	}

	// Test invalid port (should fallback to default)
	poolInvalidPort := pool.DeepCopy()
	poolInvalidPort.Spec.EndpointPickerRef.Port = &gwaiev1.Port{Number: 70000} // > 65535
	assert.Equal(t, uint32(defaultEndpointPickerPort), portForInferencePool(poolInvalidPort))

	poolNegativePort := pool.DeepCopy()
	poolNegativePort.Spec.EndpointPickerRef.Port = &gwaiev1.Port{Number: -1} // < 0 (though type is usually uint, check logic)
	// Note: gwaiev1.PortNumber is int32, so negative is possible in struct but logic handles it
	assert.Equal(t, uint32(defaultEndpointPickerPort), portForInferencePool(poolNegativePort))
}

// TestBuildAndParseMetadata_RoundTrip ensures the metadata encoding/decoding remains consistent.
// This serves as an integration check between buildEPPMetadata and getInferencePoolByMetadata.
func TestBuildAndParseMetadata_RoundTrip(t *testing.T) {
	pool := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pool",
			Namespace: "test-ns",
			Annotations: map[string]string{
				processingBodyModeAnnotation: "buffered",
				allowModeOverrideAnnotation:  "true",
			},
		},
		Spec: gwaiev1.InferencePoolSpec{
			EndpointPickerRef: &gwaiev1.EndpointPickerRef{
				Name: "test-picker",
				Port: ptr.To(gwaiev1.Port{Number: 9090}),
			},
		},
	}

	// 1. Build Metadata
	metadata := &corev3.Metadata{}
	buildEPPMetadata(metadata, pool)

	// Verify metadata structure
	filterMeta, ok := metadata.FilterMetadata[internalapi.InternalEndpointMetadataNamespace]
	assert.True(t, ok)
	assert.NotNil(t, filterMeta)

	val, ok := filterMeta.Fields[internalMetadataInferencePoolKey]
	assert.True(t, ok)
	encodedStr := val.GetStringValue()

	// Check encoded format: ns/name/svc/port/mode/override
	assert.Equal(t, "test-ns/test-pool/test-picker/9090/buffered/true", encodedStr)

	// 2. Parse Metadata back to Pool
	parsedPool := getInferencePoolByMetadata(metadata)
	assert.NotNil(t, parsedPool)

	// Verify restored properties
	assert.Equal(t, "test-pool", parsedPool.Name)
	assert.Equal(t, "test-ns", parsedPool.Namespace)
	assert.Equal(t, "test-picker", string(parsedPool.Spec.EndpointPickerRef.Name))
	assert.Equal(t, gwaiev1.PortNumber(9090), parsedPool.Spec.EndpointPickerRef.Port.Number)

	// Verify restored annotations
	extractedMode := parsedPool.Annotations[processingBodyModeAnnotation]
	extractedOverride := parsedPool.Annotations[allowModeOverrideAnnotation]
	assert.Equal(t, "buffered", extractedMode)
	assert.Equal(t, "true", extractedOverride)
}

// TestGetInferencePoolByMetadata_Malformed tests parsing of invalid metadata strings.
func TestGetInferencePoolByMetadata_Malformed(t *testing.T) {
	// Test nil metadata
	assert.Nil(t, getInferencePoolByMetadata(nil))

	// Test missing namespace
	md := &corev3.Metadata{FilterMetadata: map[string]*structpb.Struct{}}
	assert.Nil(t, getInferencePoolByMetadata(md))

	// Test invalid format string (not enough parts)
	md = &corev3.Metadata{
		FilterMetadata: map[string]*structpb.Struct{
			internalapi.InternalEndpointMetadataNamespace: {
				Fields: map[string]*structpb.Value{
					internalMetadataInferencePoolKey: structpb.NewStringValue("invalid/format/string"),
				},
			},
		},
	}
	assert.Nil(t, getInferencePoolByMetadata(md))

	// Test invalid port component
	md = &corev3.Metadata{
		FilterMetadata: map[string]*structpb.Struct{
			internalapi.InternalEndpointMetadataNamespace: {
				Fields: map[string]*structpb.Value{
					internalMetadataInferencePoolKey: structpb.NewStringValue("ns/name/svc/not-a-port/duplex/false"),
				},
			},
		},
	}
	assert.Nil(t, getInferencePoolByMetadata(md))
}

// TestBuildHTTPFilterForInferencePool_Defaults verifies default behavior separate from annotation parsing.
func TestBuildHTTPFilterForInferencePool_Defaults(t *testing.T) {
	pool := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "defaults-pool",
			Namespace: "default",
		},
		Spec: gwaiev1.InferencePoolSpec{
			EndpointPickerRef: &gwaiev1.EndpointPickerRef{Name: "default-picker"},
		},
	}

	filter := buildHTTPFilterForInferencePool(pool)
	assert.NotNil(t, filter)
	// Expect duplex by default
	assert.Equal(t, extprocv3.ProcessingMode_FULL_DUPLEX_STREAMED, filter.ProcessingMode.RequestBodyMode)
	assert.Equal(t, extprocv3.ProcessingMode_FULL_DUPLEX_STREAMED, filter.ProcessingMode.ResponseBodyMode)
	assert.False(t, filter.AllowModeOverride)
}
