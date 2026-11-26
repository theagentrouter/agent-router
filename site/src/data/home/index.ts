/**
 * Homepage content — edit copy here, not in the components.
 * Components live in src/components/home/.
 */
import vars from '@site/docs/_vars.json';

/** Selects the Envoy pods of the gateway deployed by the basic example. */
const basicGatewaySelector = 'gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic';

export type Cta = { label: string; to: string };

export const hero = {
  headline: 'Models. Tools. Agents.',
  headlineAccent: 'One router.',
  lead:
    'Your agent gets one OpenAI-compatible API for every model and one router for every MCP tool. Platform teams set up providers, credentials, limits, and failover. Envoy handles the traffic.',
  leadStrong:
    'Integrate once. Switch models without rewriting.',
  ctas: [
    { label: 'Get Started', to: '/#quickstart' },
    { label: 'View on GitHub', to: 'https://github.com/theagentrouter/agent-router' },
  ] satisfies Cta[],
  sub: 'An Agentic AI Foundation project · Powered by Envoy',
  // third path after the two OSS ones: text-only, names the provider, and
  // resolves to the vendor-neutral Solutions section
  hosted: {
    lead: 'Prefer not to run it yourself?',
    link: { label: 'Try it as a service', to: 'https://router.tetrate.ai/sign-in' },
    provider: 'hosted by Tetrate.',
    solutions: { label: 'See solutions built on Agent Router', to: '/#solutions' },
  },
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
  /** Heading beside the terminal for this tab. */
  title: string;
  /** Paragraph beside the terminal, describing this tab's path. */
  body: string;
  /** Small muted line under the CTA, e.g. who provides a hosted option. */
  note?: string;
  terminal: TerminalLine[];
};

export const quickstart = {
  label: 'Get started',
  tabs: [
    {
      id: 'local',
      title: 'Running in 60 seconds',
      label: 'On your laptop',
      cta: { label: 'Read the guide', to: '/docs/cli/aigwrun' },
      body:
        'One command runs Agent Router on your laptop — no Docker, no Kubernetes. Pick your providers, then point any OpenAI- or Anthropic-compatible client at it. The same configuration later ships to a dedicated gateway or a Kubernetes cluster.',
      terminal: [
        { kind: 'comment', text: '# 1 — install the router' },
        { kind: 'command', text: 'curl -sSL https://theagentrouter.ai/install.sh | sh' },
        { kind: 'command', text: 'export PATH="$HOME/.local/bin:$PATH"' },
        { kind: 'blank' },
        { kind: 'comment', text: '# 2 — start it, then pick your providers and paste their API keys' },
        { kind: 'command', text: 'aigw run' },
        { kind: 'blank' },
        { kind: 'comment', text: '# 3 — from another terminal, send a request (OpenAI or Tetrate Agent Router Service)' },
        { kind: 'command', text: 'curl http://localhost:1975/v1/chat/completions -H "Content-Type: application/json" \\' },
        { kind: 'command', text: `  -d '{"model": "gpt-5-mini", "messages": [{"role": "user", "content": "Hello!"}]}'` },
      ],
    },
    {
      id: 'kubernetes',
      title: 'Production on Kubernetes',
      label: 'On Kubernetes',
      cta: { label: 'Read the guide', to: '/docs/getting-started/' },
      body:
        'For production, Agent Router runs on Envoy Gateway in your Kubernetes cluster, with the same configuration you tested on your laptop. Install it with Helm, deploy a gateway, and send your first request.',
      terminal: [
        { kind: 'comment', text: '# 1 — install Envoy Gateway and Agent Router' },
        { kind: 'command', text: `helm upgrade -i eg oci://docker.io/envoyproxy/gateway-helm --version v${vars.egVersion} \\` },
        { kind: 'command', text: '  -n envoy-gateway-system --create-namespace \\' },
        {
          kind: 'command',
          text: `  -f https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/manifests/envoy-gateway-values.yaml`,
        },
        { kind: 'command', text: `helm upgrade -i aieg-crd oci://docker.io/envoyproxy/ai-gateway-crds-helm --version v${vars.aigwVersion} \\` },
        { kind: 'command', text: '  -n envoy-ai-gateway-system --create-namespace' },
        { kind: 'command', text: `helm upgrade -i aieg oci://docker.io/envoyproxy/ai-gateway-helm --version v${vars.aigwVersion} \\` },
        { kind: 'command', text: '  -n envoy-ai-gateway-system --create-namespace' },
        { kind: 'comment', text: '# wait until the pods in envoy-ai-gateway-system are ready' },
        { kind: 'blank' },
        { kind: 'comment', text: '# 2 — deploy a gateway with a mock model' },
        {
          kind: 'command',
          text: `kubectl apply -f https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/basic/basic.yaml`,
        },
        { kind: 'comment', text: '# wait until the gateway pods in envoy-gateway-system are ready' },
        { kind: 'blank' },
        { kind: 'comment', text: '# 3 — send a request' },
        { kind: 'command', text: 'kubectl port-forward -n envoy-gateway-system $(kubectl get svc -n envoy-gateway-system -o name \\' },
        { kind: 'command', text: `  -l ${basicGatewaySelector}) 8080:80 &` },
        { kind: 'command', text: 'curl -sS --retry 60 --retry-all-errors --retry-delay 2 http://localhost:8080/v1/chat/completions \\' },
        { kind: 'command', text: '  -H "Content-Type: application/json" \\' },
        {
          kind: 'command',
          text: `  -d '{"model": "some-cool-self-hosted-model", "messages": [{"role": "user", "content": "Hello!"}]}'`,
        },
      ],
    },
    {
      id: 'python',
      title: 'Call models from Python',
      label: 'From Python',
      cta: { label: 'Read the guide', to: '/docs/python-sdk' },
      body:
        'Building an agent in Python? Install the SDK and call any model with LiteLLM\'s API: the first call starts Agent Router on your laptop for you. Point the same code at your team\'s gateway with two environment variables.',
      terminal: [
        { kind: 'comment', text: '# 1 — install the Python SDK (it includes the router)' },
        { kind: 'command', text: 'pip install theagentrouter' },
        { kind: 'command', text: 'export ANTHROPIC_API_KEY=<your key>' },
        { kind: 'blank' },
        { kind: 'comment', text: '# 2 — call any model; the first call starts the router' },
        { kind: 'command', text: `python -c "import theagentrouter as ar; print(ar.completion(` },
        { kind: 'command', text: `  model='anthropic/claude-sonnet-5-5',` },
        { kind: 'command', text: `  messages=[{'role': 'user', 'content': 'Hello!'}]).choices[0].message.content)"` },
      ],
    },
    {
      id: 'hosted',
      title: 'Nothing to install',
      label: 'Hosted',
      cta: { label: 'Sign in with Google or GitHub', to: 'https://router.tetrate.ai/sign-in' },
      body:
        'Skip the install and use Agent Router as a service. Create an API key, then point your agent\'s OpenAI-compatible client at the hosted endpoint.',
      note: 'Hosted service provided by Tetrate. Nothing to install.',
      terminal: [
        { kind: 'comment', text: '# 1 — sign in at router.tetrate.ai and create an API key' },
        { kind: 'blank' },
        { kind: 'comment', text: '# 2 — the only change your agent needs' },
        { kind: 'command', text: 'export OPENAI_API_KEY=<your key>' },
        { kind: 'command', text: 'export OPENAI_BASE_URL=https://api.router.tetrate.ai/v1' },
        { kind: 'blank' },
        { kind: 'comment', text: '# 3 — send a request' },
        { kind: 'command', text: 'curl $OPENAI_BASE_URL/chat/completions \\' },
        { kind: 'command', text: '  -H "Authorization: Bearer $OPENAI_API_KEY" -H "Content-Type: application/json" \\' },
        {
          kind: 'command',
          text: `  -d '{"model": "claude-sonnet-5", "messages": [{"role": "user", "content": "Hello!"}]}'`,
        },
      ],
    },
  ] satisfies QuickstartTab[],
};

export const solutions = {
  label: 'Solutions',
  title: 'Solutions built on Agent Router',
  standfirst:
    'Commercial products and managed services that package Agent Router for teams that want support, hosting, or enterprise controls on top of the open source project.',
  cardLinkLabel: 'Learn more',
  ctaText: 'Built something on Agent Router?',
  ctaLink: {
    label: 'Add your solution →',
    to: 'https://github.com/theagentrouter/agent-router/edit/main/site/src/data/solutions/solutions.json',
  },
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
