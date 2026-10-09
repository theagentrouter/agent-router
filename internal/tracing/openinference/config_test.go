// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openinference

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewTraceConfig_Defaults(t *testing.T) {
	config := NewTraceConfig()

	require.Equal(t, defaultHideLLMInvocationParameters, config.HideLLMInvocationParameters)
	require.Equal(t, defaultHideInputs, config.HideInputs)
	require.Equal(t, defaultHideOutputs, config.HideOutputs)
	require.Equal(t, defaultHideInputMessages, config.HideInputMessages)
	require.Equal(t, defaultHideOutputMessages, config.HideOutputMessages)
	require.Equal(t, defaultHideInputImages, config.HideInputImages)
	require.Equal(t, defaultHideInputText, config.HideInputText)
	require.Equal(t, defaultHideOutputText, config.HideOutputText)
	require.Equal(t, defaultHideEmbeddingsVectors, config.HideEmbeddingsVectors)
	require.Equal(t, defaultHideEmbeddingsText, config.HideEmbeddingsText)
	require.Equal(t, defaultBase64ImageMaxLength, config.Base64ImageMaxLength)
	require.Equal(t, defaultHidePrompts, config.HidePrompts)
}

func TestNewTraceConfigFromEnv(t *testing.T) {
	tests := []struct {
		name     string
		envVars  map[string]string
		validate func(t *testing.T, config *TraceConfig)
	}{
		{
			name: "all boolean environment variables set to true",
			envVars: map[string]string{
				EnvHideLLMInvocationParameters: "true",
				EnvHideInputs:                  "true",
				EnvHideOutputs:                 "true",
				EnvHideInputMessages:           "true",
				EnvHideOutputMessages:          "true",
				EnvHideInputImages:             "true",
				EnvHideInputText:               "true",
				EnvHideOutputText:              "true",
				EnvHideEmbeddingsVectors:       "true",
				EnvHideEmbeddingsText:          "true",
				EnvHidePrompts:                 "true",
				EnvBase64ImageMaxLength:        "10000",
			},
			validate: func(t *testing.T, config *TraceConfig) {
				require.True(t, config.HideLLMInvocationParameters)
				require.True(t, config.HideInputs)
				require.True(t, config.HideOutputs)
				require.True(t, config.HideInputMessages)
				require.True(t, config.HideOutputMessages)
				require.True(t, config.HideInputImages)
				require.True(t, config.HideInputText)
				require.True(t, config.HideOutputText)
				require.True(t, config.HideEmbeddingsVectors)
				require.True(t, config.HideEmbeddingsText)
				require.True(t, config.HidePrompts)
				require.Equal(t, 10000, config.Base64ImageMaxLength)
			},
		},
		{
			name: "all boolean environment variables set to false",
			envVars: map[string]string{
				EnvHideLLMInvocationParameters: "false",
				EnvHideInputs:                  "false",
				EnvHideOutputs:                 "false",
				EnvHideInputMessages:           "false",
				EnvHideOutputMessages:          "false",
				EnvHideInputImages:             "false",
				EnvHideInputText:               "false",
				EnvHideOutputText:              "false",
				EnvHideEmbeddingsVectors:       "false",
				EnvHideEmbeddingsText:          "false",
				EnvHidePrompts:                 "false",
				EnvBase64ImageMaxLength:        "50000",
			},
			validate: func(t *testing.T, config *TraceConfig) {
				require.False(t, config.HideLLMInvocationParameters)
				require.False(t, config.HideInputs)
				require.False(t, config.HideOutputs)
				require.False(t, config.HideInputMessages)
				require.False(t, config.HideOutputMessages)
				require.False(t, config.HideInputImages)
				require.False(t, config.HideInputText)
				require.False(t, config.HideOutputText)
				require.False(t, config.HideEmbeddingsVectors)
				require.False(t, config.HideEmbeddingsText)
				require.False(t, config.HidePrompts)
				require.Equal(t, 50000, config.Base64ImageMaxLength)
			},
		},
		{
			name: "partial environment variables",
			envVars: map[string]string{
				EnvHideInputs:           "true",
				EnvHideOutputMessages:   "true",
				EnvBase64ImageMaxLength: "15000",
			},
			validate: func(t *testing.T, config *TraceConfig) {
				require.True(t, config.HideInputs)
				require.True(t, config.HideOutputMessages)
				require.Equal(t, 15000, config.Base64ImageMaxLength)
				// Others should be defaults.
				require.Equal(t, defaultHideOutputs, config.HideOutputs)
				require.Equal(t, defaultHideInputMessages, config.HideInputMessages)
				require.Equal(t, defaultHideInputText, config.HideInputText)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variables.
			for key, value := range tt.envVars {
				t.Setenv(key, value)
			}

			config := NewTraceConfigFromEnv()
			tt.validate(t, config)
		})
	}
}

func TestTraceConfig_CapturesMessages(t *testing.T) {
	tests := []struct {
		name   string
		config TraceConfig
		want   bool
	}{
		{
			name:   "defaults capture both sides",
			config: TraceConfig{},
			want:   true,
		},
		{
			name:   "input side hidden, output side still captured",
			config: TraceConfig{HideInputs: true, HideInputMessages: true},
			want:   true,
		},
		{
			name:   "output side hidden, input side still captured",
			config: TraceConfig{HideOutputs: true, HideOutputMessages: true},
			want:   true,
		},
		{
			name: "HideInputs and HideOutputs alone suppress both sides",
			config: TraceConfig{
				HideInputs:  true,
				HideOutputs: true,
			},
			want: false,
		},
		{
			name: "all four message hides suppress both sides",
			config: TraceConfig{
				HideInputs: true, HideInputMessages: true,
				HideOutputs: true, HideOutputMessages: true,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.config.CapturesMessages())
		})
	}
}
