// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package backendauth

import (
	"context"
	"fmt"
	"strings"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

type openAIHandler struct {
	accessToken string
	applyFn     applyCredentialFn
}

func newOpenAIHandler(auth *filterapi.OpenAIAuth) (filterapi.BackendAuthHandler, error) {
	return &openAIHandler{
		accessToken: strings.TrimSpace(auth.AccessToken),
		applyFn:     makeOpenAIApplyFn(auth.Organization, auth.Project),
	}, nil
}

// Do implements [Handler.Do].
//
// Sets the OpenAI access token as an authorization header, along with the organization and project
// headers when configured.
func (o *openAIHandler) Do(_ context.Context, requestHeaders map[string]string, _ []byte) ([]internalapi.Header, error) {
	return o.applyFn(requestHeaders, o.accessToken)
}

// makeOpenAIApplyFn returns an applyCredentialFn that sets Authorization: Bearer <credential> and,
// when non-empty, the OpenAI-Organization and OpenAI-Project headers.
func makeOpenAIApplyFn(organization, project string) applyCredentialFn {
	return func(requestHeaders map[string]string, credential string) ([]internalapi.Header, error) {
		v := fmt.Sprintf("Bearer %s", credential)
		requestHeaders["Authorization"] = v
		headers := []internalapi.Header{{"Authorization", v}}
		if organization != "" {
			requestHeaders["OpenAI-Organization"] = organization
			headers = append(headers, internalapi.Header{"OpenAI-Organization", organization})
		}
		if project != "" {
			requestHeaders["OpenAI-Project"] = project
			headers = append(headers, internalapi.Header{"OpenAI-Project", project})
		}
		return headers, nil
	}
}
