/**
 * Supported LLM providers, shown as the homepage logo strip.
 * Keep alphabetical. status 'in-progress' renders a "coming soon" hint.
 * Adding a provider? Follow site/static/img/providers/README.md — the docs
 * table, this list, the logo, and the repo README grid must all change together.
 */

import type { Cta } from './index';

export type Provider = {
  name: string;
  logoUrl: string;
  status: 'supported' | 'in-progress';
};

export const providers: Provider[] = [
  { name: 'Anthropic', logoUrl: '/img/providers/anthropic.svg', status: 'supported' },
  { name: 'AWS Bedrock', logoUrl: '/img/providers/aws-bedrock.svg', status: 'supported' },
  { name: 'Azure OpenAI', logoUrl: '/img/providers/azure-openai.svg', status: 'supported' },
  { name: 'BitDeer', logoUrl: '/img/providers/bitdeer.svg', status: 'supported' },
  { name: 'Cohere', logoUrl: '/img/providers/cohere.svg', status: 'supported' },
  { name: 'DeepInfra', logoUrl: '/img/providers/deepinfra.svg', status: 'supported' },
  { name: 'DeepSeek', logoUrl: '/img/providers/deepseek.svg', status: 'supported' },
  { name: 'Eden AI', logoUrl: '/img/providers/eden-ai.svg', status: 'supported' },
  { name: 'Google Gemini', logoUrl: '/img/providers/google-gemini.svg', status: 'supported' },
  { name: 'Grok', logoUrl: '/img/providers/grok.svg', status: 'supported' },
  { name: 'Groq', logoUrl: '/img/providers/groq.svg', status: 'supported' },
  { name: 'Hunyuan', logoUrl: '/img/providers/hunyuan.svg', status: 'supported' },
  { name: 'Lanseq', logoUrl: '/img/providers/lanseq.svg', status: 'supported' },
  { name: 'Mistral', logoUrl: '/img/providers/mistral.svg', status: 'supported' },
  { name: 'OpenAI', logoUrl: '/img/providers/openai.svg', status: 'supported' },
  { name: 'Paxa Labs', logoUrl: '/img/providers/paxa-labs.svg', status: 'supported' },
  { name: 'SambaNova', logoUrl: '/img/providers/sambanova.svg', status: 'supported' },
  { name: 'Tencent LLM Knowledge Engine', logoUrl: '/img/providers/tencent-lke.svg', status: 'supported' },
  { name: 'Tetrate Agent Router Service', logoUrl: '/img/providers/tars.svg', status: 'supported' },
  { name: 'Together AI', logoUrl: '/img/providers/together-ai.svg', status: 'supported' },
  { name: 'TypeSafe AI', logoUrl: '/img/providers/typesafe-ai.svg', status: 'supported' },
  { name: 'Vertex AI', logoUrl: '/img/providers/vertex-ai.svg', status: 'supported' },
];

/** Shown under the strip; the docs table has schema + auth config per provider. */
export const providersLink: Cta = {
  label: 'See all supported providers and how to configure them →',
  to: '/docs/capabilities/llm-integrations/supported-providers',
};
