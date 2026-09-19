// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package internaltesting

import (
	"cmp"
	"fmt"
	"os"
	"testing"
)

// RequiredCredential is a bit flag for the required credentials.
type RequiredCredential uint64

const (
	// RequiredCredentialOpenAI is the bit flag for the OpenAI API key.
	RequiredCredentialOpenAI RequiredCredential = 1 << iota
	// RequiredCredentialAWS is the bit flag for the AWS credentials.
	RequiredCredentialAWS
	// RequiredCredentialAzure is the bit flag for the Azure access token.
	RequiredCredentialAzure
	// RequiredCredentialGemini is the bit flag for the Gemini API key.
	RequiredCredentialGemini
	// RequiredCredentialGroq is the bit flag for the Groq API key.
	// https://console.groq.com/docs/openai
	RequiredCredentialGroq
	// RequiredCredentialGrok is the bit flag for the Grok API key.
	// https://console.groq.com/docs/openai
	RequiredCredentialGrok
	// RequiredCredentialSambaNova is the bit flag for the SambaNova API key.
	// https://docs.sambanova.ai/cloud/api-reference/endpoints/chat
	RequiredCredentialSambaNova
	// RequiredCredentialDeepInfra is the bit flag for the DeepInfra API key.
	// https://deepinfra.com/docs/openai_api
	RequiredCredentialDeepInfra
	// RequiredCredentialOpenRouter is the bit flag for the OpenRouter API key.
	// https://openrouter.ai/docs
	RequiredCredentialOpenRouter
	// RequiredCredentialAnthropic is the bit flag for the Anthropic API key.
	RequiredCredentialAnthropic
	// RequiredCredentialCohere is the bit flag for the Cohere API key.
	RequiredCredentialCohere
)

// CredentialsContext holds the context for the credentials used in the tests.
type CredentialsContext struct {
	// OpenAIValid, AWSValid, AzureValid, etc. are true if the credentials are set and ready to use the real services.
	OpenAIValid, AWSValid, AzureValid, GeminiValid, GroqValid, GrokValid, SambaNovaValid, DeepInfraValid, OpenRouterValid, AnthropicValid, CohereValid bool
	// OpenAIAPIKey is the OpenAI API key. This defaults to "dummy-openai-api-key" if not set.
	OpenAIAPIKey string
	// AnthropicAPIKey is the Anthropic API key. This defaults to "dummy-anthropic-api-key" if not set.
	AnthropicAPIKey string
	// CohereAPIKey is the Cohere API key. This defaults to "dummy-cohere-api-key" if not set.
	CohereAPIKey string
	// AWSFileLiteral contains the AWS credentials in the format of a file literal.
	AWSFileLiteral     string
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	// AzureAccessToken is the Azure access token. This defaults to "dummy-azure-access-token" if not set.
	AzureAccessToken string
	// GeminiAPIKey is the API key for Gemini API. https://ai.google.dev/gemini-api/docs/openai
	GeminiAPIKey string
	// GroqAPIKey is the API key for Groq API. https://console.groq.com/docs/openai
	GroqAPIKey string
	// GrokAPIKey is the API key for Grok API. https://console.grok.com/docs/openai
	GrokAPIKey string
	// SambaNovaAPIKey is the API key for SambaNova API. https://docs.sambanova.ai/cloud/api-reference/endpoints/chat
	SambaNovaAPIKey string
	// DeepInfraAPIKey is the API key for DeepInfra API. https://deepinfra.com/docs/openai_api
	DeepInfraAPIKey string
	// OpenRouterAPIKey is the API key for OpenRouter API. https://openrouter.ai/docs
	OpenRouterAPIKey string
}

// MaybeSkip skips the test if the required credentials are not set.
func (c CredentialsContext) MaybeSkip(t testing.TB, required RequiredCredential) { //nolint:gocritic
	if required&RequiredCredentialOpenAI != 0 && !c.OpenAIValid {
		t.Skip("skipping test as OpenAI API key is not set in TEST_OPENAI_API_KEY")
	}
	if required&RequiredCredentialAWS != 0 && !c.AWSValid {
		t.Skip("skipping test as AWS credentials are not set in TEST_AWS_ACCESS_KEY_ID and TEST_AWS_SECRET_ACCESS_KEY")
	}
	if required&RequiredCredentialAzure != 0 && !c.AzureValid {
		t.Skip("skipping test as Azure credentials are not set in TEST_AZURE_ACCESS_TOKEN")
	}
	if required&RequiredCredentialGemini != 0 && !c.GeminiValid {
		t.Skip("skipping test as Gemini API key is not set in TEST_GEMINI_API_KEY")
	}
	if required&RequiredCredentialGroq != 0 && !c.GroqValid {
		t.Skip("skipping test as Groq API key is not set in TEST_GROQ_API_KEY")
	}
	if required&RequiredCredentialGrok != 0 && !c.GrokValid {
		t.Skip("skipping test as Grok API key is not set in TEST_GROK_API_KEY")
	}
	if required&RequiredCredentialSambaNova != 0 && !c.SambaNovaValid {
		t.Skip("skipping test as SambaNova API key is not set in TEST_SAMBANOVA_API_KEY")
	}
	if required&RequiredCredentialDeepInfra != 0 && !c.DeepInfraValid {
		t.Skip("skipping test as DeepInfra API key is not set in TEST_DEEPINFRA_API_KEY")
	}
	if required&RequiredCredentialOpenRouter != 0 && !c.OpenRouterValid {
		t.Skip("skipping test as OpenRouter API key is not set in TEST_OPENROUTER_API_KEY")
	}
	if required&RequiredCredentialAnthropic != 0 && !c.AnthropicValid {
		t.Skip("skipping test as Anthropic API key is not set in TEST_ANTHROPIC_API_KEY")
	}
	if required&RequiredCredentialCohere != 0 && !c.CohereValid {
		t.Skip("skipping test as Cohere API key is not set in TEST_COHERE_API_KEY")
	}
}

// RequireNewCredentialsContext creates a new credential context for the tests from the environment variables.
func RequireNewCredentialsContext() (ctx CredentialsContext) {
	// Set up credential file for OpenAI.
	openAIAPIKeyEnv := os.Getenv("TEST_OPENAI_API_KEY")
	ctx.OpenAIValid = openAIAPIKeyEnv != ""
	ctx.OpenAIAPIKey = cmp.Or(openAIAPIKeyEnv, "dummy-openai-api-key")

	// Set up credential file for Gemini API.
	geminiAPIKeyEnv := os.Getenv("TEST_GEMINI_API_KEY")
	ctx.GeminiValid = geminiAPIKeyEnv != ""
	ctx.GeminiAPIKey = cmp.Or(geminiAPIKeyEnv, "dummy-gemini-api-key")

	// Set up credential file for Groq API.
	groqAPIKeyEnv := os.Getenv("TEST_GROQ_API_KEY")
	ctx.GroqValid = groqAPIKeyEnv != ""
	ctx.GroqAPIKey = cmp.Or(groqAPIKeyEnv, "dummy-groq-api-key")

	// Set up credential file for Grok API.
	grokAPIKeyEnv := os.Getenv("TEST_GROK_API_KEY")
	ctx.GrokValid = grokAPIKeyEnv != ""
	ctx.GrokAPIKey = cmp.Or(grokAPIKeyEnv, "dummy-grok-api-key")

	// Set up credential file for SambaNova API.
	sambaNovaAPIKeyEnv := os.Getenv("TEST_SAMBANOVA_API_KEY")
	ctx.SambaNovaValid = sambaNovaAPIKeyEnv != ""
	ctx.SambaNovaAPIKey = cmp.Or(sambaNovaAPIKeyEnv, "dummy-sambanova-api-key")

	// Set up credential file for DeepInfra API.
	deepInfraAPIKeyEnv := os.Getenv("TEST_DEEPINFRA_API_KEY")
	ctx.DeepInfraValid = deepInfraAPIKeyEnv != ""
	ctx.DeepInfraAPIKey = cmp.Or(deepInfraAPIKeyEnv, "dummy-deepinfra-api-key")

	// Set up credential for OpenRouter API.
	openRouterAPIKeyEnv := os.Getenv("TEST_OPENROUTER_API_KEY")
	ctx.OpenRouterValid = openRouterAPIKeyEnv != ""
	ctx.OpenRouterAPIKey = cmp.Or(openRouterAPIKeyEnv, "dummy-openrouter-api-key")

	// Set up credential file for Azure.
	azureAccessTokenEnv := os.Getenv("TEST_AZURE_ACCESS_TOKEN")
	ctx.AzureValid = azureAccessTokenEnv != ""
	azureAccessToken := cmp.Or(azureAccessTokenEnv, "dummy-azure-access-token")
	ctx.AzureAccessToken = azureAccessToken

	// Set up credential file for AWS.
	awsAccessKeyID := os.Getenv("TEST_AWS_ACCESS_KEY_ID")
	awsSecretAccessKey := os.Getenv("TEST_AWS_SECRET_ACCESS_KEY")
	awsSessionToken := os.Getenv("TEST_AWS_SESSION_TOKEN")
	ctx.AWSValid = awsAccessKeyID != "" && awsSecretAccessKey != ""
	ctx.AWSAccessKeyID = cmp.Or(awsAccessKeyID, "dummy_access_key_id")
	ctx.AWSSecretAccessKey = cmp.Or(awsSecretAccessKey, "dummy_secret_access_key")
	if awsSessionToken != "" {
		ctx.AWSFileLiteral = fmt.Sprintf("[default]\nAWS_ACCESS_KEY_ID=%s\nAWS_SECRET_ACCESS_KEY=%s\nAWS_SESSION_TOKEN=%s\n",
			ctx.AWSAccessKeyID, ctx.AWSSecretAccessKey, awsSessionToken,
		)
	} else {
		ctx.AWSFileLiteral = fmt.Sprintf("[default]\nAWS_ACCESS_KEY_ID=%s\nAWS_SECRET_ACCESS_KEY=%s\n",
			ctx.AWSAccessKeyID, ctx.AWSSecretAccessKey,
		)
	}

	// Set up credential file for Anthropic.
	anthropicAPIKeyEnv := os.Getenv("TEST_ANTHROPIC_API_KEY")
	ctx.AnthropicValid = anthropicAPIKeyEnv != ""
	ctx.AnthropicAPIKey = cmp.Or(anthropicAPIKeyEnv, "dummy-anthropic-api-key")
	// Set up credential for Cohere.
	cohereAPIKeyEnv := os.Getenv("TEST_COHERE_API_KEY")
	ctx.CohereValid = cohereAPIKeyEnv != ""
	ctx.CohereAPIKey = cmp.Or(cohereAPIKeyEnv, "dummy-cohere-api-key")
	return
}
