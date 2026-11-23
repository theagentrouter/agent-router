// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/envoyproxy/ai-gateway/internal/autoconfig"
)

// catchAllModels matches every model name.
const catchAllModels = autoconfig.AllModels

// providerOption is a provider that can be configured in the interactive setup.
type providerOption struct {
	name        string // Resource name in the generated configuration.
	title       string // Name shown to the user.
	schema      string // autoconfig schema name.
	baseURL     string // Default base URL. Empty means it must be entered.
	askBaseURL  bool   // Whether to prompt for the base URL.
	apiKey      bool   // Whether an API key is required.
	keyEnv      string // Environment variable the saved configuration reads the API key from.
	optionalKey bool   // Whether to prompt for an optional API key.
	keyHint     string // Where to get an API key.
	models      string // Regular expression of the models it serves, or catchAllModels.
	example     string // Example model name, for the usage hint.
	path        string // Path clients use to reach it, for the usage hint.
	azure       bool   // Whether it needs the Azure-specific prompts.
}

// interactiveProviders are the providers offered by the interactive setup.
var interactiveProviders = []providerOption{
	{
		name: "openai", keyEnv: "OPENAI_API_KEY", title: "OpenAI", schema: "OpenAI", baseURL: "https://api.openai.com/v1",
		apiKey: true, keyHint: "https://platform.openai.com/api-keys",
		models: "(gpt-|chatgpt-|o[0-9]|text-embedding-|dall-e-|whisper-|tts-).*", example: "gpt-5-mini",
		path: "/v1/chat/completions",
	},
	{
		name: "anthropic", keyEnv: "ANTHROPIC_API_KEY", title: "Anthropic", schema: "Anthropic", baseURL: "https://api.anthropic.com/v1",
		apiKey: true, keyHint: "https://console.anthropic.com/settings/keys",
		models: autoconfig.ClaudeModels, example: "claude-sonnet-4-5", path: "/anthropic/v1/messages",
	},
	{
		name: "tars", keyEnv: "TARS_API_KEY", title: "Tetrate Agent Router Service", schema: "OpenAI", baseURL: "https://api.router.tetrate.ai/v1",
		apiKey: true, keyHint: "https://router.tetrate.ai",
		models: catchAllModels, example: "gpt-5-mini", path: "/v1/chat/completions",
	},
	{
		name: "azure-openai", keyEnv: "AZURE_OPENAI_API_KEY", title: "Azure OpenAI", schema: "AzureOpenAI", askBaseURL: true,
		apiKey: true, keyHint: "the \"Keys and Endpoint\" page of your Azure OpenAI resource",
		models: catchAllModels, path: "/v1/chat/completions", azure: true,
	},
	{
		name: "ollama", title: "Ollama (local models)", schema: "OpenAI", baseURL: "http://localhost:11434/v1", askBaseURL: true,
		models: catchAllModels, example: "qwen2.5:0.5b", path: "/v1/chat/completions",
	},
	{
		name: "openai-compatible", keyEnv: "OPENAI_COMPATIBLE_API_KEY", title: "Other OpenAI-compatible API", schema: "OpenAI", askBaseURL: true,
		optionalKey: true, models: catchAllModels, example: "<model>", path: "/v1/chat/completions",
	},
}

// interactiveSetup configures the fallback used by readConfig when no provider is configured.
type interactiveSetup struct {
	// prompt asks the user for the providers and adds them to the configuration.
	prompt func(data *autoconfig.ConfigData) error
	// savePath is where readConfig saves the generated configuration. Empty means it is not saved.
	savePath string
	// ran is set when the setup completed and generated the configuration.
	ran bool
}

// configuredProvider is a provider with the values entered by the user.
type configuredProvider struct {
	providerOption
	autoconfig.Provider
	url    string
	apiKey string // The API key entered, or empty for none.
}

// runInteractiveSetup asks the user which providers to use and their credentials,
// and adds them to data. The configuration reads the API keys from environment variables,
// so they are never written to disk: it returns the values of those variables, which the
// caller must set before rendering it, and whether the user chose to save it to savePath.
func runInteractiveSetup(ui *terminalUI, data *autoconfig.ConfigData, savePath string) (save bool, env map[string]string, err error) {
	ui.printf("\n%s\n", ui.cyan("╭─ Agent Router setup ─────────────────────────────────────────────────"))
	ui.printf("%s No configuration found. Choose the providers to route to and enter their credentials.\n", ui.cyan("│"))
	ui.printf("%s %s\n", ui.cyan("│"), ui.dim("To skip this, set OPENAI_API_KEY or ANTHROPIC_API_KEY, or run \"aigw run <config file>\"."))
	ui.printf("%s\n\n", ui.cyan("╰──────────────────────────────────────────────────────────────────────"))

	items := make([]selectItem, len(interactiveProviders))
	for i := range interactiveProviders {
		p := &interactiveProviders[i]
		detail := strings.TrimPrefix(strings.TrimPrefix(p.baseURL, "https://"), "http://")
		if p.azure || detail == "" {
			detail = "your own endpoint"
		}
		items[i] = selectItem{label: p.title, detail: detail}
	}
	chosen, err := ui.selectMany("Which providers do you want to use?", items)
	if err != nil {
		return false, nil, err
	}

	var providers []configuredProvider
	for _, i := range chosen {
		p, promptErr := promptProvider(ui, &interactiveProviders[i])
		if promptErr != nil {
			return false, nil, promptErr
		}
		providers = append(providers, p)
	}

	if err = assignModelRoutes(ui, providers); err != nil {
		return false, nil, err
	}
	env = map[string]string{}
	for i := range providers {
		p := &providers[i]
		if p.apiKey != "" {
			p.APIKeyEnv = p.keyEnv
			env[p.keyEnv] = p.apiKey
		}
		if err = autoconfig.AddProvider(data, &p.Provider, p.url); err != nil {
			return false, nil, fmt.Errorf("invalid %s configuration: %w", p.title, err)
		}
	}

	ui.printf("\n")
	save, err = ui.confirm("Save this configuration for the next runs? API keys are not saved", true)
	if err != nil {
		return false, nil, err
	}
	printSetupSummary(ui, providers, savePath, save)
	return save, env, nil
}

// promptProvider asks for the settings of a single provider.
func promptProvider(ui *terminalUI, opt *providerOption) (configuredProvider, error) {
	p := configuredProvider{
		providerOption: *opt,
		Provider:       autoconfig.Provider{Name: opt.name, SchemaName: opt.schema, ModelMatch: opt.models},
		url:            opt.baseURL,
	}
	ui.printf("\n  %s\n", ui.bold(opt.title))
	if opt.keyHint != "" {
		ui.printf("    %s\n", ui.dim("Get an API key at "+opt.keyHint+"."))
	}

	var err error
	if opt.askBaseURL {
		label := "Base URL"
		if opt.azure {
			label = "Endpoint (https://<resource>.openai.azure.com)"
		}
		if p.url, err = requiredInput(ui, label, opt.baseURL, false, validURL); err != nil {
			return p, err
		}
	}
	if opt.azure {
		if p.Version, err = requiredInput(ui, "API version", "2024-12-01-preview", false, nil); err != nil {
			return p, err
		}
		deployments, inputErr := ui.input("Deployment names, comma-separated "+ui.dim("(empty routes all models here)"), "", false)
		if inputErr != nil {
			return p, inputErr
		}
		if names := splitList(deployments); len(names) > 0 {
			p.ModelMatch = modelsRegex(names)
			p.example = names[0]
		}
	}
	switch {
	case opt.apiKey:
		p.apiKey, err = requiredInput(ui, "API key", "", true, nil)
	case opt.optionalKey:
		p.apiKey, err = ui.input("API key "+ui.dim("(empty for none)"), "", true)
	}
	return p, err
}

// assignModelRoutes makes sure every provider gets some traffic. A single provider gets
// every model. When several providers serve any model, only the last one can be the
// catch-all, so the user is asked which models to route to the others.
func assignModelRoutes(ui *terminalUI, providers []configuredProvider) error {
	if len(providers) == 1 {
		providers[0].ModelMatch = catchAllModels
		return nil
	}
	var catchAll []int
	for i := range providers {
		if providers[i].ModelMatch == catchAllModels {
			catchAll = append(catchAll, i)
		}
	}
	for _, i := range catchAll[:max(len(catchAll)-1, 0)] {
		p := &providers[i]
		ui.printf("\n  %s\n", ui.bold("Models for "+p.title))
		ui.printf("    %s\n", ui.dim("Requests are routed by model name, and only one provider can receive all the other models."))
		answer, err := requiredInput(ui, "Model names to route to "+p.title+", comma-separated", "", false, nil)
		if err != nil {
			return err
		}
		names := splitList(answer)
		p.ModelMatch = modelsRegex(names)
		p.example = names[0]
	}
	// Providers serving any model go last, so they don't shadow the ones serving specific models.
	slices.SortStableFunc(providers, func(a, b configuredProvider) int {
		return cmp.Compare(boolInt(a.ModelMatch == catchAllModels), boolInt(b.ModelMatch == catchAllModels))
	})
	return nil
}

// printSetupSummary shows how requests will be routed and how to try them.
func printSetupSummary(ui *terminalUI, providers []configuredProvider, savePath string, saved bool) {
	bar := ui.cyan("│")
	ui.printf("\n%s\n", ui.cyan("╭─ Agent Router configured ────────────────────────────────────────────"))
	ui.printf("%s Requests are routed by model name:\n", bar)
	models := make([]string, len(providers))
	width := 0
	for i := range providers {
		models[i] = describeModels(providers[i].ModelMatch)
		if len(providers) == 1 {
			models[i] = "any model"
		}
		width = max(width, len(models[i]))
	}
	for i := range providers {
		ui.printf("%s   %s%s  → %s\n", bar, models[i], strings.Repeat(" ", width-len(models[i])), providers[i].title)
	}
	ui.printf("%s\n%s Try it once Agent Router is listening, from another terminal:\n", bar, bar)
	// Show one example per API: Anthropic models are served on the Anthropic Messages API only.
	shown := map[string]bool{}
	for i := range providers {
		p := &providers[i]
		if shown[p.path] {
			continue
		}
		shown[p.path] = true
		ui.printf("%s   curl http://localhost:1975%s -H \"Content-Type: application/json\" \\\n", bar, p.path)
		if p.schema == "Anthropic" {
			ui.printf("%s     -d '{\"model\": \"%s\", \"max_tokens\": 256, \"messages\": [{\"role\": \"user\", \"content\": \"Hello!\"}]}'\n", bar, p.example)
		} else {
			ui.printf("%s     -d '{\"model\": \"%s\", \"messages\": [{\"role\": \"user\", \"content\": \"Hello!\"}]}'\n", bar, p.example)
		}
	}
	ui.printf("%s\n", bar)
	if saved {
		ui.printf("%s Configuration saved to %s.\n", bar, savePath)
		ui.printf("%s %s\n", bar, ui.dim("The next \"aigw run\" uses it. Edit it to fine-tune routing, or delete it to run this setup again."))
		var exports []string
		for i := range providers {
			if providers[i].APIKeyEnv != "" {
				exports = append(exports, fmt.Sprintf("export %s=<API key>  %s", providers[i].APIKeyEnv, ui.dim("# "+providers[i].title)))
			}
		}
		if len(exports) > 0 {
			ui.printf("%s\n%s %s\n", bar, bar, ui.bold("API keys are not saved."))
			ui.printf("%s The configuration reads them from environment variables. Set them before the next run,\n", bar)
			ui.printf("%s for example in your shell profile (~/.zshrc, ~/.bashrc):\n", bar)
			for _, e := range exports {
				ui.printf("%s   %s\n", bar, e)
			}
		}
	} else {
		ui.printf("%s %s\n", bar, ui.dim("The configuration was not saved: this setup runs again the next time."))
	}
	ui.printf("%s\n\n", ui.cyan("╰──────────────────────────────────────────────────────────────────────"))
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// requiredInput prompts until a non-empty, valid value is entered.
func requiredInput(ui *terminalUI, label, def string, secret bool, validate func(string) error) (string, error) {
	for {
		v, err := ui.input(label, def, secret)
		if err != nil {
			return "", err
		}
		switch {
		case v == "":
			ui.printf("    %s\n", ui.sgr("33", "A value is required."))
		case validate != nil && validate(v) != nil:
			ui.printf("    %s\n", ui.sgr("33", validate(v).Error()))
		default:
			return v, nil
		}
	}
}

func validURL(s string) error {
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return fmt.Errorf("enter a URL starting with http:// or https://")
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// modelsRegex returns a regular expression matching exactly the given model names.
func modelsRegex(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = regexp.QuoteMeta(n)
	}
	return strings.Join(quoted, "|")
}

// describeModels renders a model regular expression for the summary.
func describeModels(match string) string {
	if match == catchAllModels {
		return "any other model"
	}
	return match
}
