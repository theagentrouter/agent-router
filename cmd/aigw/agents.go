// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/envoyproxy/ai-gateway/internal/autoconfig"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

const (
	// agentHeader tags the requests of each coding agent, so the shared gateway routes them to
	// the provider chosen for that agent.
	agentHeader = "x-aigw-agent"
	// agentGatewayURL is where the gateway started for the coding agents listens.
	agentGatewayURL = "http://localhost:1975"
	// agentRequestTimeout bounds a single agent request, including a long streamed response.
	agentRequestTimeout = "30m"
	// tetrateRouterURL is the default Tetrate Agent Router endpoint.
	tetrateRouterURL = "https://api.router.tetrate.ai"
	// tetrateKeyEnv is the environment variable holding the Tetrate Agent Router API key.
	tetrateKeyEnv = "TARS_API_KEY" //nolint:gosec // G101: an environment variable name, not a credential.
	// tetratePassthroughHeader authenticates with Tetrate Agent Router when the request carries
	// the user's own Claude subscription credentials.
	tetratePassthroughHeader = "x-tars-api-key" //nolint:gosec // G101: a header name, not a credential.
)

// agentMode is one way a coding agent can reach its models.
type agentMode struct {
	id     string // Saved in agents.yaml.
	title  string // Shown to the user.
	detail string // Shown next to the title in the picker.
	group  string // Modes in the same group share a first-level choice, refined by a second question.
	short  string // Name of the mode within its group, for messages.
	schema string // autoconfig schema name.
	// baseURL is the default endpoint. The provider's URL is the endpoint followed by apiPath.
	baseURL string
	apiPath string
	askURL  bool   // Whether to ask for the endpoint.
	urlHint string // Label of the endpoint question.
	keyEnv  string // Environment variable holding the API key. Empty means no key.
	// keyOptional means the API key may be left empty, to send no credentials.
	keyOptional bool
	keyLabel    string // Label of the API key question.
	keyHint     string // Where to get an API key.
	// keyHeader, when set, sends the API key in this header and forwards the agent's own
	// credentials unchanged, instead of replacing them with the API key.
	keyHeader string
	// subscription means the agent authenticates with its own login, which the gateway forwards.
	subscription bool
	auth         string // How requests are authenticated, for the summary.
}

// agentSpec describes a coding agent that "aigw <agent>" runs through Agent Router.
type agentSpec struct {
	name    string // Command name, and the value of agentHeader.
	title   string // Shown to the user.
	binary  string // Executable to run.
	install string // Where to get the executable.
	what    string // What the agent reaches, for the picker question.
	modes   []agentMode
	// groups titles the first-level choices that are refined by a second question.
	groups map[string]selectItem
	// groupQuestion is the second question asked for a group.
	groupQuestion string
}

var claudeAgent = agentSpec{
	name: "claude", title: "Claude Code", binary: "claude", what: "Claude",
	install: "https://docs.claude.com/en/docs/claude-code/setup",
	modes: []agentMode{
		{
			id: "subscription", title: "Claude subscription", detail: "Your Claude Pro/Max login, direct to Anthropic",
			schema: "Anthropic", baseURL: "https://api.anthropic.com", apiPath: "/v1", subscription: true,
			auth: "your Claude subscription. Agent Router forwards Claude Code's login unchanged and never sees an API key.",
		},
		{
			id: "anthropic-api-key", title: "Anthropic API key", detail: "Billed to your Anthropic Console account",
			schema: "Anthropic", baseURL: "https://api.anthropic.com", apiPath: "/v1",
			keyEnv: "ANTHROPIC_API_KEY", keyLabel: "API key", keyHint: "Get an API key at https://console.anthropic.com/settings/keys.",
			auth: "Agent Router adds your Anthropic API key. Claude Code never gets it, and your Claude login (if any) is not used.",
		},
		{
			id: "tetrate-passthrough", group: "tetrate", short: "passthrough", title: "My Claude subscription",
			detail: "Passthrough: your Claude Pro/Max login; Agent Router routes and observes (no token margin)",
			schema: "Anthropic", baseURL: tetrateRouterURL, apiPath: "/v1", askURL: true, urlHint: "Agent Router URL",
			keyEnv: tetrateKeyEnv, keyLabel: "Agent Router API key", keyHint: "Get an Agent Router API key from your Agent Router console.",
			keyHeader: tetratePassthroughHeader, subscription: true,
			auth: "your Claude subscription, forwarded unchanged to Anthropic. Agent Router adds your Agent Router API key " +
				"(" + tetratePassthroughHeader + ") for routing and observability. Requests show as \"passthrough\" in its logs.",
		},
		{
			id: "tetrate-managed", group: "tetrate", short: "managed", title: "Tetrate Agent Router's Anthropic key",
			detail: "Managed: Agent Router's Anthropic key, billed through Agent Router",
			schema: "Anthropic", baseURL: tetrateRouterURL, apiPath: "/v1", askURL: true, urlHint: "Agent Router URL",
			keyEnv: tetrateKeyEnv, keyLabel: "Agent Router API key", keyHint: "Get an Agent Router API key from your Agent Router console.",
			auth: "Agent Router authenticates with your Agent Router API key; Tetrate Agent Router calls Anthropic with its managed key. " +
				"Claude Code never gets the key, and your Claude login is not used.",
		},
	},
	groups: map[string]selectItem{
		"tetrate": {label: "Tetrate Agent Router", detail: "Through Tetrate Agent Router (Service or Enterprise)"},
	},
	groupQuestion: "How should Claude Code sign in to Claude?",
}

var codexAgent = agentSpec{
	name: "codex", title: "Codex", binary: "codex", what: "its models",
	install: "https://developers.openai.com/codex/cli",
	modes: []agentMode{
		{
			id: "openai-api-key", title: "OpenAI API key", detail: "Billed to your OpenAI Platform account",
			schema: "OpenAI", baseURL: "https://api.openai.com", apiPath: "/v1",
			keyEnv: "OPENAI_API_KEY", keyLabel: "API key", keyHint: "Get an API key at https://platform.openai.com/api-keys.",
			auth: "Agent Router adds your OpenAI API key. Codex never gets it.",
		},
		{
			id: "tetrate", title: "Tetrate Agent Router", detail: "Through Tetrate Agent Router (Service or Enterprise)",
			schema: "OpenAI", baseURL: tetrateRouterURL, apiPath: "/v1", askURL: true, urlHint: "Agent Router URL",
			keyEnv: tetrateKeyEnv, keyLabel: "Agent Router API key", keyHint: "Get an Agent Router API key from your Agent Router console.",
			auth: "Agent Router adds your Agent Router API key. Codex never gets it.",
		},
		{
			id: "openai-compatible", title: "Other OpenAI-compatible API", detail: "Any API serving the OpenAI Responses API",
			schema: "OpenAI", askURL: true, urlHint: "Base URL (for example http://localhost:11434/v1)",
			keyEnv: "OPENAI_COMPATIBLE_API_KEY", keyOptional: true, keyLabel: "API key",
			auth: "Agent Router adds the API key, if any. Codex never gets it.",
		},
	},
}

// agents are the coding agents "aigw" can run, by command name.
var agents = map[string]*agentSpec{claudeAgent.name: &claudeAgent, codexAgent.name: &codexAgent}

// agentSettings is how an agent reaches its models, as saved in agents.yaml. It never holds API keys.
type agentSettings struct {
	Mode  string `json:"mode"`
	URL   string `json:"url,omitempty"`
	NoKey bool   `json:"noKey,omitempty"`
}

// agentsConfigPath is where the agent settings are saved.
func agentsConfigPath(configHome string) string { return filepath.Join(configHome, "agents.yaml") }

// loadAgentSettings reads the saved settings of every agent. A missing file means none.
func loadAgentSettings(path string) (map[string]agentSettings, error) {
	settings := map[string]agentSettings{}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return settings, nil
	} else if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	if err = yaml.Unmarshal(raw, &settings); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	return settings, nil
}

const agentsConfigHeader = `# How "aigw claude" and "aigw codex" reach their models. It contains no API keys.
# Run "aigw <agent> --setup" to change it.
`

// saveAgentSettings writes the settings of every agent.
func saveAgentSettings(path string, settings map[string]agentSettings) error {
	raw, err := yaml.Marshal(settings)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	if err = os.WriteFile(path, append([]byte(agentsConfigHeader), raw...), 0o600); err != nil {
		return fmt.Errorf("failed to save %s: %w", path, err)
	}
	return nil
}

// mode returns the mode with the given id.
func (a *agentSpec) mode(id string) (*agentMode, error) {
	for i := range a.modes {
		if a.modes[i].id == id {
			return &a.modes[i], nil
		}
	}
	return nil, fmt.Errorf("unknown %s mode %q: run \"aigw %s --setup\" to choose again", a.title, id, a.name)
}

// sameEndpoint reports whether the mode with the given id sends requests to the same place as m:
// it is m, or a mode of the same group. The URL of another mode must not be reused, or the
// API key of m could be sent to it.
func (a *agentSpec) sameEndpoint(id string, m *agentMode) bool {
	other, err := a.mode(id)
	return err == nil && (other.id == m.id || (m.group != "" && other.group == m.group))
}

// endpoint returns the endpoint the agent's requests are sent to.
func (m *agentMode) endpoint(s agentSettings) string {
	if s.URL != "" {
		return s.URL
	}
	return m.baseURL
}

// keyEnvFor returns the environment variable holding the API key, or "" if none is used.
func (m *agentMode) keyEnvFor(s agentSettings) string {
	if s.NoKey {
		return ""
	}
	return m.keyEnv
}

// host returns the host of the endpoint, for messages.
func (m *agentMode) host(s agentSettings) string {
	u, err := url.Parse(m.endpoint(s))
	if err != nil || u.Host == "" {
		return m.endpoint(s)
	}
	return u.Host
}

// describe summarizes the settings, for messages.
func (a *agentSpec) describe(s agentSettings) string {
	m, err := a.mode(s.Mode)
	if err != nil {
		return s.Mode
	}
	if m.group != "" {
		return a.groups[m.group].label + ", " + m.short + " (" + m.host(s) + ")"
	}
	return m.title + " (" + m.host(s) + ")"
}

// addProvider adds the provider serving the agent's requests to data.
func (a *agentSpec) addProvider(data *autoconfig.ConfigData, s agentSettings) error {
	m, err := a.mode(s.Mode)
	if err != nil {
		return err
	}
	p := autoconfig.Provider{
		Name:           "agent-" + a.name,
		SchemaName:     m.schema,
		ModelMatch:     autoconfig.AllModels,
		HeaderMatch:    &autoconfig.Header{Name: agentHeader, Value: a.name},
		RequestTimeout: agentRequestTimeout,
		RemoveHeaders:  []string{agentHeader},
	}
	keyEnv := m.keyEnvFor(s)
	if m.subscription {
		// The agent's own credentials go through untouched, and the API key, if any, goes in its own header.
		if keyEnv != "" {
			p.Headers = []autoconfig.Header{{Name: m.keyHeader, Value: "${" + keyEnv + "}"}}
		}
	} else {
		// The gateway owns the credentials, if any: drop the placeholder ones the agent sends.
		// That includes a passthrough key the user's own settings may add, which would switch
		// Tetrate Agent Router to passthrough mode with no Claude login to pass through.
		p.APIKeyEnv = keyEnv
		p.RemoveHeaders = append(p.RemoveHeaders, "authorization", "x-api-key", tetratePassthroughHeader)
	}
	endpoint := strings.TrimSuffix(m.endpoint(s), "/") + m.apiPath
	if err = autoconfig.AddProvider(data, &p, endpoint); err != nil {
		return fmt.Errorf("invalid %s configuration: %w. Run \"aigw %s --setup\" to choose again", a.title, err, a.name)
	}
	return nil
}

// missingKey returns the environment variable that the settings need and is not set, or "".
func (a *agentSpec) missingKey(s agentSettings, keys map[string]string) string {
	m, err := a.mode(s.Mode)
	if err != nil {
		return ""
	}
	if keyEnv := m.keyEnvFor(s); keyEnv != "" && a.key(s, keys) == "" {
		return keyEnv
	}
	return ""
}

// key returns the API key the settings use: the one entered during the setup, if any, or the
// one in the environment. It is "" if the settings use none, or it is not set.
func (a *agentSpec) key(s agentSettings, keys map[string]string) string {
	m, err := a.mode(s.Mode)
	if err != nil {
		return ""
	}
	keyEnv := m.keyEnvFor(s)
	if keyEnv == "" {
		return ""
	}
	if k := keys[keyEnv]; k != "" {
		return k
	}
	return os.Getenv(keyEnv)
}

// command returns the arguments and environment to run the agent through the gateway.
func (a *agentSpec) command(s agentSettings, args, environ []string) ([]string, []string, error) {
	m, err := a.mode(s.Mode)
	if err != nil {
		return nil, nil, err
	}
	// The agent never gets the API keys of the providers: the gateway adds them. That also keeps
	// Claude Code from sending ANTHROPIC_API_KEY instead of its login, or the gateway's credentials.
	env := slices.DeleteFunc(slices.Clone(environ), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return slices.Contains(providerKeyEnvs(), k)
	})
	switch a.name {
	case claudeAgent.name:
		headers := agentHeader + ": " + a.name
		if existing := lookupEnv(env, "ANTHROPIC_CUSTOM_HEADERS"); existing != "" {
			headers = existing + "\n" + headers
		}
		env = setEnv(env, "ANTHROPIC_BASE_URL", agentGatewayURL+"/anthropic")
		env = setEnv(env, "ANTHROPIC_CUSTOM_HEADERS", headers)
		settingsEnv := map[string]string{
			"ANTHROPIC_BASE_URL":       agentGatewayURL + "/anthropic",
			"ANTHROPIC_CUSTOM_HEADERS": headers,
		}
		if m.subscription {
			env = setEnv(env, "ANTHROPIC_AUTH_TOKEN", "")
		} else {
			// Claude Code needs some credential to send requests: the gateway replaces it.
			env = setEnv(env, "ANTHROPIC_AUTH_TOKEN", "aigw")
			settingsEnv["ANTHROPIC_AUTH_TOKEN"] = "aigw"
		}
		// The "env" of the user's Claude Code settings overrides the process environment, and may
		// point elsewhere: the settings given on the command line take precedence over them.
		settings, err := json.Marshal(map[string]any{"env": settingsEnv})
		if err != nil {
			return nil, nil, err
		}
		return append([]string{"--settings", string(settings)}, args...), env, nil
	case codexAgent.name:
		env = setEnv(env, "AIGW_CODEX_API_KEY", "aigw")
		overrides := []string{
			"-c", `model_provider="aigw"`,
			"-c", `model_providers.aigw.name="Agent Router"`,
			"-c", `model_providers.aigw.base_url="` + agentGatewayURL + `/v1"`,
			"-c", `model_providers.aigw.wire_api="responses"`,
			"-c", `model_providers.aigw.env_key="AIGW_CODEX_API_KEY"`,
			"-c", `model_providers.aigw.http_headers={"` + agentHeader + `"="` + a.name + `"}`,
		}
		return append(overrides, args...), env, nil
	default:
		return nil, nil, fmt.Errorf("unknown agent %q", a.name)
	}
}

// providerKeyEnvs returns the environment variables holding the API keys of every mode.
func providerKeyEnvs() []string {
	var envs []string
	for _, a := range agents {
		for i := range a.modes {
			if k := a.modes[i].keyEnv; k != "" && !slices.Contains(envs, k) {
				envs = append(envs, k)
			}
		}
	}
	return envs
}

// lookupEnv returns the value of key in env, a list of KEY=value entries.
func lookupEnv(env []string, key string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// setEnv returns env with key set to value. An empty value removes key.
func setEnv(env []string, key, value string) []string {
	env = slices.DeleteFunc(env, func(kv string) bool { return strings.HasPrefix(kv, key+"=") })
	if value != "" {
		env = append(env, key+"="+value)
	}
	return env
}

// runAgentSetup asks how the agent reaches its models. It returns the settings, and the API key
// entered, if any, keyed by the environment variable that holds it.
func runAgentSetup(ui *terminalUI, a *agentSpec, current *agentSettings) (agentSettings, map[string]string, error) {
	bar := ui.cyan("│")
	if current == nil {
		ui.printf("\n%s\n", ui.cyan(boxTitle("Agent Router for "+a.title)))
		ui.printf("%s First run: choose how %s reaches %s. It's remembered;\n", bar, a.title, a.what)
		ui.printf("%s run \"aigw %s --setup\" to change it.\n", bar, a.name)
		ui.printf("%s\n\n", ui.cyan(boxBottom))
	} else {
		ui.printf("\n  %s %s\n\n", ui.dim("Currently:"), a.describe(*current))
	}

	// The first question lists the modes without a group, and one entry per group.
	type choice struct {
		mode  int    // Index of the mode, or -1 for a group.
		group string // Group of the choice, if any.
	}
	var (
		items   []selectItem
		choices []choice
	)
	def := 0
	for i := range a.modes {
		m := &a.modes[i]
		if m.group == "" {
			if current != nil && current.Mode == m.id {
				def = len(items)
			}
			items = append(items, selectItem{label: m.title, detail: m.detail})
			choices = append(choices, choice{mode: i})
			continue
		}
		if slices.ContainsFunc(choices, func(c choice) bool { return c.group == m.group }) {
			if current != nil && current.Mode == m.id {
				def = slices.IndexFunc(choices, func(c choice) bool { return c.group == m.group })
			}
			continue
		}
		if current != nil && current.Mode == m.id {
			def = len(items)
		}
		items = append(items, a.groups[m.group])
		choices = append(choices, choice{mode: -1, group: m.group})
	}
	picked, err := ui.selectOne("How should "+a.title+" reach "+a.what+"?", items, def)
	if err != nil {
		return agentSettings{}, nil, err
	}

	var (
		settings agentSettings
		mode     *agentMode
	)
	if c := choices[picked]; c.group == "" {
		mode = &a.modes[c.mode]
	} else {
		// Ask the endpoint first, shared by the modes of the group, then which mode.
		var groupModes []*agentMode
		var groupItems []selectItem
		groupDef := 0
		for i := range a.modes {
			if a.modes[i].group == c.group {
				if current != nil && current.Mode == a.modes[i].id {
					groupDef = len(groupModes)
				}
				groupModes = append(groupModes, &a.modes[i])
				groupItems = append(groupItems, selectItem{label: a.modes[i].title, detail: a.modes[i].detail})
			}
		}
		ui.printf("\n  %s\n", ui.bold(a.groups[c.group].label))
		if settings.URL, err = askURL(ui, a, groupModes[0], current); err != nil {
			return agentSettings{}, nil, err
		}
		ui.printf("\n")
		if picked, err = ui.selectOne(a.groupQuestion, groupItems, groupDef); err != nil {
			return agentSettings{}, nil, err
		}
		mode = groupModes[picked]
		ui.printf("\n")
	}
	settings.Mode = mode.id

	if mode.group == "" && (mode.askURL || mode.keyEnv != "") {
		ui.printf("\n  %s\n", ui.bold(mode.title))
		if mode.askURL {
			if settings.URL, err = askURL(ui, a, mode, current); err != nil {
				return agentSettings{}, nil, err
			}
		}
	}
	keys := map[string]string{}
	if mode.keyEnv != "" {
		switch {
		case os.Getenv(mode.keyEnv) != "":
			ui.printf("    %s\n", ui.dim("Using "+mode.keyEnv+" from the environment."))
		case mode.keyOptional:
			key, inputErr := ui.input(mode.keyLabel+" "+ui.dim("(empty for none)"), "", true)
			if inputErr != nil {
				return agentSettings{}, nil, inputErr
			}
			if key == "" {
				settings.NoKey = true
			} else {
				keys[mode.keyEnv] = key
			}
		default:
			if mode.keyHint != "" {
				ui.printf("    %s\n", ui.dim(mode.keyHint))
			}
			key, inputErr := requiredInput(ui, mode.keyLabel, "", true, nil)
			if inputErr != nil {
				return agentSettings{}, nil, inputErr
			}
			keys[mode.keyEnv] = key
		}
	}
	return settings, keys, nil
}

// askURL asks for the endpoint of a mode. The default is the current one, if the current mode
// sends requests to the same place, or the mode's default.
// It returns "" when the answer is the mode's default, so the saved settings follow it.
func askURL(ui *terminalUI, a *agentSpec, m *agentMode, current *agentSettings) (string, error) {
	def := m.baseURL
	if current != nil && current.URL != "" && a.sameEndpoint(current.Mode, m) {
		def = current.URL
	}
	v, err := requiredInput(ui, m.urlHint, def, false, validURL)
	if err != nil || v == m.baseURL {
		return "", err
	}
	return strings.TrimSuffix(v, "/"), nil
}

// printAgentSummary shows how the agent reaches its models once configured.
func printAgentSummary(ui *terminalUI, a *agentSpec, s agentSettings, keys map[string]string, savePath string) {
	m, err := a.mode(s.Mode)
	if err != nil {
		return
	}
	bar := ui.cyan("│")
	ui.printf("\n%s\n", ui.cyan(boxTitle(a.title+" is ready")))
	via := "Agent Router"
	if m.group != "" || m.askURL {
		via = "Agent Router (local)"
	}
	ui.printf("%s %s → %s → %s\n", bar, a.title, via, m.host(s))
	for i, line := range wrap("Auth: "+m.auth, 70) {
		if i > 0 {
			line = "      " + line
		}
		ui.printf("%s %s\n", bar, line)
	}
	if m.subscription {
		ui.printf("%s %s\n", bar, ui.dim("Not logged in yet? "+a.title+" asks you to log in when it starts."))
	}
	ui.printf("%s\n%s Saved to %s %s.\n", bar, bar, savePath, ui.dim("(no secrets)"))
	if len(keys) > 0 {
		ui.printf("%s\n%s %s Set this before the next run,\n", bar, bar, ui.bold("API keys are not saved."))
		ui.printf("%s for example in your shell profile (~/.zshrc, ~/.bashrc):\n", bar)
		for k := range keys {
			ui.printf("%s   export %s=<API key>\n", bar, k)
		}
	}
	ui.printf("%s\n\n", ui.cyan(boxBottom))
}

const boxBottom = "╰──────────────────────────────────────────────────────────────────────"

// boxTitle returns the top line of a box with the given title.
func boxTitle(title string) string {
	line := "╭─ " + title + " "
	return line + strings.Repeat("─", max(len([]rune(boxBottom))-len([]rune(line)), 3))
}

// wrap splits s into lines of at most width characters, at spaces.
func wrap(s string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(s) {
		if line != "" && len([]rune(line))+1+len([]rune(word)) > width {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	return append(lines, line)
}
