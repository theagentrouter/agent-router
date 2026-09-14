/**
 * Homepage content — edit copy here, not in the components.
 * Components live in src/components/home/.
 */

export type Cta = { label: string; to: string };

export const hero = {
  headline: 'Models. Tools. Agents.',
  headlineAccent: 'One router.',
  lead:
    'Your agent gets one OpenAI-compatible API for every model and one router for every MCP tool. Platform teams set up providers, credentials, limits, and failover. Envoy handles the traffic.',
  leadStrong:
    'Integrate once. Switch models without rewriting.',
  ctas: [
    { label: 'Get Started', to: '/docs/getting-started/' },
    { label: 'View on GitHub', to: 'https://github.com/theagentrouter/agent-router' },
  ] satisfies Cta[],
  sub: 'An Agentic AI Foundation project · Powered by Envoy',
};

export const howItFits = {
  label: 'How it fits together',
  title: 'Powerful traffic handling, made usable',
  standfirst:
    'Your agent speaks one API. You describe providers, credentials, tools, and limits once; Agent Router turns that into Envoy configuration, and Envoy handles every request with the reliability it has proven in production for a decade.',
  planes: [
    {
      name: 'Agent Router',
      role: 'Configures — providers, credentials, routing, tools, and limits',
      image: { light: '/img/brand/ar-mark-marquee.svg', dark: '/img/brand/ar-mark-marquee.svg' },
    },
    {
      name: 'Envoy',
      role: 'Handles the traffic — the proxy that already carries much of the world\'s production traffic',
      // official CNCF icon-only cuts (cncf/artwork) — never recoloured
      image: { light: '/img/brand/envoy-icon-color.svg', dark: '/img/brand/envoy-icon-white.svg' },
    },
  ],
  refrainStrong: 'Agent Router configures.',
  refrain: 'Envoy handles the traffic.',
};

export type CapabilityIcon =
  | 'hexagon'
  | 'lanes'
  | 'lock'
  | 'hub'
  | 'bars'
  | 'clock';

export type Capability = {
  icon: CapabilityIcon;
  title: string;
  body: string;
  to?: string;
};

export const capabilities = {
  label: 'Capabilities',
  title: 'One integration point for your agent',
  standfirst:
    'Provider APIs, credentials, failover, tool wiring, and token limits move out of your agent and into configuration — on the traffic handling Envoy has proven in production for a decade, without requiring Envoy expertise.',
  items: [
    {
      icon: 'hexagon',
      title: 'One API, every provider',
      body: 'Route OpenAI-compatible requests to Anthropic, Bedrock, Vertex, Azure, or self-hosted vLLM. Switching models is changing the model name.',
      to: '/docs/capabilities/llm-integrations/',
    },
    {
      icon: 'lanes',
      title: 'Traffic management',
      body: 'Provider fallback, model name virtualization, and token limits per team, app, or model. Your agent never counts a token.',
      to: '/docs/capabilities/traffic/',
    },
    {
      icon: 'lock',
      title: 'Credentials stay out of your code',
      body: 'Your agent authenticates to the router and nothing else. Provider API keys and AWS, GCP, and Azure credentials stay with Agent Router, rotated in one place.',
      to: '/docs/capabilities/security/',
    },
    {
      icon: 'hub',
      title: 'One router for every MCP tool',
      body: 'One tool catalog from many MCP servers, filtered by who is asking — sized for each agent, so context stays small and wrong-tool calls stay rare.',
      to: '/docs/capabilities/mcp/',
    },
    {
      icon: 'bars',
      title: 'Inference-aware routing',
      body: 'Route to self-managed models with Kubernetes InferencePool support and inference-aware endpoint picking.',
      to: '/docs/capabilities/inference/',
    },
    {
      icon: 'clock',
      title: 'Observability',
      body: 'See what your agent actually did — which model answered, how many tokens, time to first token, where a fallback kicked in — with no instrumentation in your agent. OpenTelemetry GenAI conventions.',
      to: '/docs/capabilities/observability/',
    },
  ] satisfies Capability[],
};

export type TerminalLine = { kind: 'comment' | 'command' | 'blank'; text?: string };

export type QuickstartTab = {
  id: string;
  label: string;
  cta: Cta;
  terminal: TerminalLine[];
};

export const quickstart = {
  label: 'Get running',
  title: 'From laptop to production',
  body:
    'One command on your laptop. The same configuration ships to a dedicated gateway or a Kubernetes cluster — only the packaging changes.',
  tabs: [
    {
      id: 'local',
      label: 'On your laptop',
      cta: { label: 'Read the guide', to: '/docs/cli/aigwrun' },
      terminal: [
        { kind: 'comment', text: '# 1 — start the router locally' },
        { kind: 'command', text: 'OPENAI_API_KEY=sk-... aigw run' },
        { kind: 'blank' },
        { kind: 'comment', text: '# 2 — the only change your agent needs' },
        { kind: 'command', text: 'export OPENAI_BASE_URL=http://localhost:1975/v1' },
        { kind: 'blank' },
        { kind: 'comment', text: '# 3 — add MCP tools (same mcpServers file as Cursor / Claude Desktop)' },
        { kind: 'command', text: 'aigw run --mcp-config mcp-servers.json' },
      ],
    },
    {
      id: 'kubernetes',
      label: 'On Kubernetes',
      cta: { label: 'Read the guide', to: '/docs/getting-started/' },
      terminal: [
        { kind: 'comment', text: '# 1 — install the gateway' },
        { kind: 'command', text: 'helm install aigw oci://docker.io/envoyproxy/ai-gateway-helm \\' },
        { kind: 'command', text: '  --namespace envoy-ai-gateway-system --create-namespace' },
        { kind: 'blank' },
        { kind: 'comment', text: '# 2 — declare a route' },
        { kind: 'command', text: 'kubectl apply -f aigatewayroute.yaml' },
        { kind: 'blank' },
        { kind: 'comment', text: '# 3 — send a request' },
        { kind: 'command', text: 'curl $GATEWAY/v1/chat/completions \\' },
        { kind: 'command', text: `  -d '{"model": "claude-sonnet-5", "messages": [...]}'` },
      ],
    },
  ] satisfies QuickstartTab[],
};

export type CommunityIcon = 'chat' | 'calendar' | 'github' | 'mic';

export const community = {
  ctaText: 'Using Agent Router?',
  ctaLink: {
    label: 'Add your logo →',
    to: 'https://github.com/theagentrouter/agent-router/edit/main/site/src/data/adopters/adopters.json',
  },
  pills: [
    { icon: 'chat', label: 'Join the Discord', to: 'https://discord.gg/xuxtPq43gZ' },
    {
      icon: 'calendar',
      label: 'Weekly meeting — Mondays',
      to: 'https://zoom-lfx.platform.linuxfoundation.org/meeting/91546415944?password=61fd5a5d-41e9-4b0c-86ea-b607c4513e37',
    },
    {
      icon: 'github',
      label: 'GitHub Discussions',
      to: 'https://github.com/theagentrouter/agent-router/issues?q=is%3Aissue+label%3Adiscussion',
    },
    { icon: 'mic', label: 'Talks', to: '/talks' },
  ] satisfies { icon: CommunityIcon; label: string; to: string }[],
};
