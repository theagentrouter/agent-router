import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

// This runs in Node.js - Don't use client-side code here (browser APIs, JSX...)

const config: Config = {
  title: 'Agent Router',
  tagline: 'The open source control plane for agentic traffic, powered by Envoy. Formerly Envoy AI Gateway.',
  favicon: 'img/brand/favicon.svg',

  scripts: [
    {
      src: "https://widget.kapa.ai/kapa-widget.bundle.js",
      // TODO(launch): website-id still points at the Envoy AI Gateway kapa
      // project (answers come from the old-domain index until kapa re-crawls
      // theagentrouter.ai). Update together with the kapa ticket.
      "data-website-id": "b232d295-36d9-4b0f-95da-a4524b622ef0",
      "data-project-name": "Agent Router",
      "data-modal-disclaimer-text": "This AI assistant can help you find information about Agent Router (formerly Envoy AI Gateway), Envoy Gateway and Proxy. Please verify important information from our official documentation.",
      "data-modal-example-questions": "How do I install Agent Router?, How do I connect to LLM Providers?, How do I configure token rate limits?, What LLM Providers are supported?",
      "data-modal-ask-ai-button-text": "Ask AI",
      "data-modal-title": "Agent Router Assistant",
      "data-project-logo": "/img/brand/ar-mark-marquee.svg",
      // teal ground: the Ember mark may not sit on Marquee orange (kit rule
      // — the front hexagon vanishes); teal is an approved logo ground.
      "data-button-bg-color": "#0B3B33",
      "data-button-hover-bg-color": "#116355",
      "data-button-text-color": "#FFFFFF",
      "data-button-border": "none",
      "data-button-width": "90px",
      "data-button-height": "100px",
      "data-button-position-bottom": "24px",
      "data-button-position-right": "24px",
      "data-button-z-index": "1000",
      "data-button-image-height": "50px",
      "data-button-image-width": "50px",
      "data-button-border-radius": "8px",
      "data-button-padding": "5px",
      "data-button-hover-animation-enabled": "true",
      "data-button-animation-enabled": "false",
      "data-search-mode-enabled": "true",
      "data-search-include-source-names": '["Envoy AI Gateway"]',
      "data-answer-cta-button-enabled": "true",
      "data-answer-cta-button-link": "https://github.com/theagentrouter/agent-router/discussions/new?category=q-a",
      "data-answer-cta-button-text": "Need more help? Ask a human!",
      "data-modal-disclaimer": "This AI assistant can help you find information about **Agent Router** (formerly Envoy AI Gateway), **Envoy Gateway** and **Proxy**. Please verify important information from our official documentation. If you need more help you can always [ask a human](https://github.com/theagentrouter/agent-router/discussions/new?category=q-a).",
      "data-modal-override-open-id": "custom-ask-ai-button",
      "data-mcp-enabled": "true",
      "data-mcp-server-url": "https://envoy-gateway.mcp.kapa.ai",
      "data-user-analytics-fingerprint-enabled": "true",
      "data-modal-full-screen": "true",
      "data-modal-z-index": "2000",
      async: true,
    },
  ],

  url: 'https://theagentrouter.ai',
  baseUrl: '/',

  organizationName: 'theagentrouter',
  projectName: 'site',

  onBrokenLinks: 'throw',

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  markdown: {
    mermaid: true,
    hooks: {
      onBrokenMarkdownLinks: 'warn'
    },
  },

  themes: ['@docusaurus/theme-mermaid'],

  plugins: [
    './src/plugins/latestBlogsPlugin.ts',
  ],

  headTags: [
    {
      tagName: 'link',
      attributes: {
        rel: 'preload',
        href: '/fonts/Inter-Variable.woff2',
        as: 'font',
        type: 'font/woff2',
        crossorigin: 'anonymous',
      },
    },
    {
      tagName: 'link',
      attributes: {
        rel: 'preload',
        href: '/fonts/Archivo-Variable.woff2',
        as: 'font',
        type: 'font/woff2',
        crossorigin: 'anonymous',
      },
    },
  ],

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.ts',
          remarkPlugins: [
            [require('@docusaurus/theme-mermaid'), {}],
          ],
          lastVersion: '1.1',
          versions: {
            current: {
              label: 'Next',
              path: 'next',
              banner: 'unreleased'
            },
            '1.1': {
              label: '1.1',
              path: '/',
              banner: 'none'
            },
            '1.0': {
              label: '1.0',
              path: '1.0',
              banner: 'unmaintained'
            },
            '0.7': {
              label: '0.7',
              path: '0.7',
              banner: 'unmaintained'
            },
            '0.6': {
              label: '0.6',
              path: '0.6',
              banner: 'unmaintained'
            },
            '0.5': {
              label: '0.5',
              path: '0.5',
              banner: 'unmaintained'
            },
            '0.4': {
              label: '0.4',
              path: '0.4',
              banner: 'unmaintained'
            },
            '0.3': {
              label: '0.3',
              path: '0.3',
              banner: 'unmaintained'
            },
            '0.2': {
              label: '0.2',
              path: '0.2',
              banner: 'unmaintained'
            },
            '0.1': {
              label: '0.1',
              path: '0.1',
              banner: 'unmaintained'
            },
          },
        },
        blog: {
          path: 'blog',
          showReadingTime: true,
          feedOptions: {
            type: ['rss', 'atom'],
            xslt: true,
          },
          onInlineTags: 'warn',
          onInlineAuthors: 'warn',
          onUntruncatedBlogPosts: 'warn',
          blogSidebarTitle: 'Blog posts',
          blogSidebarCount: 'ALL',
        },
        theme: {
          customCss: [
            './src/css/brand/tokens.css',
            './src/css/brand/ar-pattern.css',
            './src/css/fonts.css',
            './src/css/custom.css',
          ],
        },
        // TODO(launch): create a GA4 property for theagentrouter.ai and
        // replace this tracking ID (currently the Envoy AI Gateway property).
        gtag: {
          trackingID: 'G-DXJEH1ZRXX',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    colorMode: {
      defaultMode: 'light',
      disableSwitch: false,
      respectPrefersColorScheme: true,
    },
    announcementBar: {
      id: 'agent_router_rebrand',
      content:
        '<b class="announceFormer">Formerly Envoy AI Gateway</b> — now an Agentic AI Foundation project. Same code, same maintainers. <a href="/blog/envoy-ai-gateway-is-now-agent-router">Read the announcement →</a>',
      backgroundColor: 'var(--ar-cream-2)',
      textColor: 'var(--ar-ink-600)',
      isCloseable: true,
    },
    image: 'img/brand/og-card@1200.png',
    navbar: {
      logo: {
        alt: 'Agent Router',
        src: 'img/brand/ar-horizontal-primary.svg',
        srcDark: 'img/brand/ar-horizontal-on-dark.svg',
      },
      items: [
        {
          label: 'Docs',
          to: '/docs',
          position: 'left',
        },
        {
          label: 'Blog',
          to: '/blog',
          position: 'left',
        },
        {
          label: 'Release Notes',
          to: '/release-notes/',
          position: 'right',
        },
        {
          label: 'Community',
          position: 'right',
          items: [
            {
              label: 'Join us on Discord',
              href: 'https://discord.gg/xuxtPq43gZ',
            },
            {
              label: 'Weekly Meeting Notes (Mondays)',
              href: 'https://docs.google.com/document/d/10e1sfsF-3G3Du5nBHGmLjXw5GVMqqCvFDqp_O65B0_w/edit?tab=t.0',
            },
            {
              label: 'GitHub Discussions',
              href: 'https://github.com/theagentrouter/agent-router/issues?q=is%3Aissue+label%3Adiscussion',
            },
            {
              label: 'Talks and Presentations',
              to: '/talks',
            },
          ],
        },
        {
          type: 'docsVersionDropdown',
        },
        {
          href: 'https://github.com/theagentrouter/agent-router',
          label: 'GitHub',
          position: 'right',
        }
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Project',
          items: [
            {
              label: 'Docs',
              to: '/docs',
            },
            {
              label: 'Blog',
              to: '/blog',
            },
            {
              label: 'Release Notes',
              to: '/release-notes/',
            },
            {
              label: 'Talks',
              to: '/talks',
            },
            {
              label: 'Trademark Policy',
              to: '/trademark-policy',
            },
          ],
        },
        {
          title: 'Community',
          items: [
            {
              label: 'Discord',
              href: 'https://discord.gg/xuxtPq43gZ',
            },
            {
              label: 'Weekly Meeting (Mondays)',
              href: 'https://zoom-lfx.platform.linuxfoundation.org/meeting/91546415944?password=61fd5a5d-41e9-4b0c-86ea-b607c4513e37',
            },
            {
              label: 'GitHub Discussions',
              href: 'https://github.com/theagentrouter/agent-router/issues?q=is%3Aissue+label%3Adiscussion',
            },
            {
              label: 'LinkedIn',
              href: 'https://www.linkedin.com/company/envoy-cloud-native',
            },
          ],
        },
        {
          title: 'Built on Envoy',
          items: [
            {
              label: 'Envoy Gateway',
              href: 'https://gateway.envoyproxy.io',
            },
            {
              label: 'Envoy Proxy',
              href: 'https://envoyproxy.io',
            },
            {
              label: 'GitHub',
              href: 'https://github.com/theagentrouter/agent-router',
            },
          ],
        },
      ],
      logo: {
        alt: 'Agent Router',
        src: 'img/brand/ar-mark-marquee.svg',
        width: 72,
      },
      // Required LF footer, verbatim per the LF onboarding email.
      copyright: `Agent Router is an Agentic AI Foundation project, powered by Envoy.<br/>Copyright © Agent Router a Series of LF Projects, LLC<br/>For web site terms of use, <a href="/trademark-policy">trademark policy</a> and other project policies please see <a href="https://lfprojects.org">https://lfprojects.org</a>.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
    },
    mermaid: {
      theme: {light: 'neutral', dark: 'dark'},
      options: {
        themeVariables: {
          primaryColor: '#FFE4D6',
          primaryBorderColor: '#B83700',
          primaryTextColor: '#12100E',
          lineColor: '#4A423C',
          fontFamily: 'Inter, ui-sans-serif, sans-serif',
        },
      },
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
