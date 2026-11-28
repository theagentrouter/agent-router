// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package gcp

// InteractionsRequest holds the fields of a Gemini Interactions API create request
// (POST /interactions) that the gateway reads. All other fields are forwarded unchanged.
//
// https://docs.cloud.google.com/gemini-enterprise-agent-platform/reference/models/interactions-api
type InteractionsRequest struct {
	// Model is the model of a model interaction.
	Model string `json:"model,omitempty"`
	// Agent is the agent of an agent interaction (e.g. "antigravity-preview-09-2026"). It is used
	// when Model is empty.
	Agent string `json:"agent,omitempty"`
	// Stream requests a server-sent event stream.
	Stream bool `json:"stream,omitempty"`
	// Store asks the provider to keep the interaction. The provider default is true.
	Store *bool `json:"store,omitempty"`
	// Background runs the interaction asynchronously. It requires Store.
	Background *bool `json:"background,omitempty"`
	// PreviousInteractionID continues a stored interaction. It requires Store.
	PreviousInteractionID string `json:"previous_interaction_id,omitempty"`
}

// RoutingModel returns the name the request is routed by: Model, or Agent when Model is empty.
func (r *InteractionsRequest) RoutingModel() string {
	if r.Model != "" {
		return r.Model
	}
	return r.Agent
}
