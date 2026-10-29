// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package guardrails

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

type bedrockEvaluator struct {
	config      *filterapi.BedrockGuardrailProvider
	client      *http.Client
	credentials aws.CredentialsProvider
	signer      *awsv4.Signer
}

func newBedrockEvaluator(ctx context.Context, config *filterapi.BedrockGuardrailProvider, client *http.Client) (filterapi.GuardrailEvaluator, error) {
	if config == nil || config.Region == "" || config.GuardrailIdentifier == "" || config.GuardrailVersion == "" {
		return nil, fmt.Errorf("bedrock region, guardrailIdentifier, and guardrailVersion are required")
	}
	credentials, err := loadAWSCredentials(ctx, config)
	if err != nil {
		return nil, err
	}
	configCopy := *config
	if configCopy.Endpoint == "" {
		configCopy.Endpoint = fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com", configCopy.Region)
	}
	return &bedrockEvaluator{config: &configCopy, client: client, credentials: credentials, signer: awsv4.NewSigner()}, nil
}

func loadAWSCredentials(ctx context.Context, guardrailConfig *filterapi.BedrockGuardrailProvider) (aws.CredentialsProvider, error) {
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(guardrailConfig.Region)}
	var credentialsFile *os.File
	if guardrailConfig.CredentialFileLiteral != "" {
		var err error
		credentialsFile, err = os.CreateTemp("", "guardrail-aws-credentials")
		if err != nil {
			return nil, fmt.Errorf("cannot create temporary AWS credentials file: %w", err)
		}
		defer func() {
			_ = credentialsFile.Close()
			_ = os.Remove(credentialsFile.Name())
		}()
		if _, err = credentialsFile.WriteString(guardrailConfig.CredentialFileLiteral); err != nil {
			return nil, fmt.Errorf("cannot write AWS credentials file: %w", err)
		}
		options = append(options, awsconfig.WithSharedCredentialsFiles([]string{credentialsFile.Name()}))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("cannot load AWS config: %w", err)
	}
	if credentialsFile == nil {
		return cfg.Credentials, nil
	}
	credentials, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot load AWS credentials: %w", err)
	}
	return aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return credentials, nil
	}), nil
}

func (e *bedrockEvaluator) Evaluate(ctx context.Context, body []byte, phase filterapi.GuardrailPhase) (filterapi.GuardrailEvaluationResult, error) {
	source := "INPUT"
	if phase == filterapi.GuardrailPhaseResponse {
		source = "OUTPUT"
	}
	payload, err := json.Marshal(struct {
		Source  string `json:"source"`
		Content []struct {
			Text struct {
				Text string `json:"text"`
			} `json:"text"`
		} `json:"content"`
	}{Source: source, Content: []struct {
		Text struct {
			Text string `json:"text"`
		} `json:"text"`
	}{{Text: struct {
		Text string `json:"text"`
	}{Text: string(body)}}}})
	if err != nil {
		return filterapi.GuardrailEvaluationResult{}, err
	}
	endpoint := strings.TrimRight(e.config.Endpoint, "/") + "/guardrail/" + url.PathEscape(e.config.GuardrailIdentifier) + "/version/" + url.PathEscape(e.config.GuardrailVersion) + "/apply"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return filterapi.GuardrailEvaluationResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	credentials, err := e.credentials.Retrieve(ctx)
	if err != nil {
		return filterapi.GuardrailEvaluationResult{}, fmt.Errorf("cannot retrieve AWS credentials: %w", err)
	}
	payloadHash := sha256.Sum256(payload)
	if err = e.signer.SignHTTP(ctx, credentials, req, hex.EncodeToString(payloadHash[:]), "bedrock", e.config.Region, time.Now()); err != nil {
		return filterapi.GuardrailEvaluationResult{}, fmt.Errorf("cannot sign Bedrock request: %w", err)
	}
	result := struct {
		Action  string `json:"action"`
		Outputs []struct {
			Text string `json:"text"`
		} `json:"outputs"`
	}{}
	if err = sendJSON(req, e.client, &result); err != nil {
		return filterapi.GuardrailEvaluationResult{}, fmt.Errorf("bedrock ApplyGuardrail request failed: %w", err)
	}
	evaluation := filterapi.GuardrailEvaluationResult{Matched: result.Action == "GUARDRAIL_INTERVENED"}
	if len(result.Outputs) > 0 {
		evaluation.Replacement = []byte(result.Outputs[0].Text)
	}
	return evaluation, nil
}
