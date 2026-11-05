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
	"github.com/stretchr/testify/require"
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

	// Check encoded format: ns/name/svc/port/mode/override. An unset failureMode is FailClose, which
	// keeps the 6-field format.
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
	assert.Equal(t, gwaiev1.EndpointPickerFailClose, parsedPool.Spec.EndpointPickerRef.FailureMode)
}

func TestBuildAndParseMetadata_FailureMode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		failureMode gwaiev1.EndpointPickerFailureMode
		wantEncoded string
		want        gwaiev1.EndpointPickerFailureMode
	}{
		{name: "FailOpen", failureMode: gwaiev1.EndpointPickerFailOpen, wantEncoded: "ns/pool/picker/9002/duplex/false/FailOpen", want: gwaiev1.EndpointPickerFailOpen},
		// FailClose keeps the 6-field format that an older extension server can still read.
		{name: "FailClose", failureMode: gwaiev1.EndpointPickerFailClose, wantEncoded: "ns/pool/picker/9002/duplex/false", want: gwaiev1.EndpointPickerFailClose},
		{name: "unset", failureMode: "", wantEncoded: "ns/pool/picker/9002/duplex/false", want: gwaiev1.EndpointPickerFailClose},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := &gwaiev1.InferencePool{
				ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: "ns"},
				Spec: gwaiev1.InferencePoolSpec{
					EndpointPickerRef: &gwaiev1.EndpointPickerRef{Name: "picker", FailureMode: tc.failureMode},
				},
			}
			metadata := &corev3.Metadata{}
			buildEPPMetadata(metadata, pool)
			encoded := metadata.FilterMetadata[internalapi.InternalEndpointMetadataNamespace].Fields[internalMetadataInferencePoolKey].GetStringValue()
			assert.Equal(t, tc.wantEncoded, encoded)

			parsed := getInferencePoolByMetadata(metadata)
			require.NotNil(t, parsed)
			assert.Equal(t, tc.want, parsed.Spec.EndpointPickerRef.FailureMode)
		})
	}
}

// TestGetInferencePoolByMetadata_FieldCount verifies which metadata formats are accepted. Metadata
// written before failureMode was added (6 fields) still parses, as FailClose, because during a rolling
// upgrade clusters and routes may carry either format.
func TestGetInferencePoolByMetadata_FieldCount(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata string
		wantNil  bool
		want     gwaiev1.EndpointPickerFailureMode
	}{
		{name: "6 fields is FailClose", metadata: "ns/name/svc/9002/duplex/false", want: gwaiev1.EndpointPickerFailClose},
		{name: "7 fields FailOpen", metadata: "ns/name/svc/9002/duplex/false/FailOpen", want: gwaiev1.EndpointPickerFailOpen},
		{name: "7 fields FailClose", metadata: "ns/name/svc/9002/duplex/false/FailClose", want: gwaiev1.EndpointPickerFailClose},
		// Anything that is not exactly FailOpen must not make a pool fail open.
		{name: "7 fields unknown value is FailClose", metadata: "ns/name/svc/9002/duplex/false/Garbage", want: gwaiev1.EndpointPickerFailClose},
		{name: "7 fields empty value is FailClose", metadata: "ns/name/svc/9002/duplex/false/", want: gwaiev1.EndpointPickerFailClose},
		{name: "5 fields", metadata: "ns/name/svc/9002/duplex", wantNil: true},
		{name: "8 fields", metadata: "ns/name/svc/9002/duplex/false/FailOpen/extra", wantNil: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			md := &corev3.Metadata{
				FilterMetadata: map[string]*structpb.Struct{
					internalapi.InternalEndpointMetadataNamespace: {
						Fields: map[string]*structpb.Value{
							internalMetadataInferencePoolKey: structpb.NewStringValue(tc.metadata),
						},
					},
				},
			}
			pool := getInferencePoolByMetadata(md)
			if tc.wantNil {
				require.Nil(t, pool)
				return
			}
			require.NotNil(t, pool)
			assert.Equal(t, "name", pool.Name)
			assert.Equal(t, "ns", pool.Namespace)
			assert.Equal(t, tc.want, pool.Spec.EndpointPickerRef.FailureMode)
			assert.Equal(t, tc.want == gwaiev1.EndpointPickerFailOpen, inferencePoolFailsOpen(pool))
		})
	}
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
	// An unset failureMode is FailClose: the request fails when the endpoint picker is unreachable.
	assert.False(t, filter.FailureModeAllow)
}

// TestBuildHTTPFilterForInferencePool_FailureMode verifies that the endpoint picker ext_proc filter
// lets a request continue when the picker is unreachable only for a FailOpen pool.
func TestBuildHTTPFilterForInferencePool_FailureMode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		failureMode gwaiev1.EndpointPickerFailureMode
		want        bool
	}{
		{name: "FailOpen", failureMode: gwaiev1.EndpointPickerFailOpen, want: true},
		{name: "FailClose", failureMode: gwaiev1.EndpointPickerFailClose, want: false},
		{name: "unset", failureMode: "", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := &gwaiev1.InferencePool{
				ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: "ns"},
				Spec: gwaiev1.InferencePoolSpec{
					EndpointPickerRef: &gwaiev1.EndpointPickerRef{Name: "picker", FailureMode: tc.failureMode},
				},
			}
			filter := buildHTTPFilterForInferencePool(pool)
			assert.Equal(t, tc.want, filter.FailureModeAllow)
			// The FailOpen cluster reads the picker's choice from envoy.lb dynamic metadata, which
			// ext_proc only accepts from the processor when the namespace is listed.
			if tc.want {
				require.NotNil(t, filter.MetadataOptions)
				assert.Equal(t, []string{"envoy.lb"}, filter.MetadataOptions.GetReceivingNamespaces().GetUntyped())
			} else {
				assert.Nil(t, filter.MetadataOptions)
			}
		})
	}
}
