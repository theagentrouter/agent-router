# theagentrouter.ai

The website for **Agent Router** — the open source control plane for GenAI and
agent traffic, formerly **Envoy AI Gateway**, now an Agentic AI Foundation
project. Built with [Docusaurus](https://docusaurus.io/).

This directory is the `site/` folder of the main
[agent-router](https://github.com/theagentrouter/agent-router) repository; Netlify
builds it from the repo-root `netlify.toml` (base `site/`).

## Development

Requires Node.js 22+ (see `.nvmrc`).

```
npm install
npm run start        # dev server with hot reload
npm run build        # production build (broken links fail the build)
npm run serve        # serve the production build
npm run typecheck    # TypeScript check
npm run check:brand  # rename-safety assertions (see AGENTS.md)
```

## Where things live

- `docs/` — current ("Next") docs; `versioned_docs/` + `versions.json` — released versions (1.1 renders at `/docs/`)
- `blog/` — posts under `blog/YYYY/`; authors in `blog/authors.yml`
- `src/data/home/` — **all homepage copy** (hero, capabilities, quickstart, community, providers)
- `src/components/home/` — homepage section components (one directory per section, CSS modules)
- `src/data/` — adopters, talks, release-notes data (JSON)
- `src/css/brand/` — vendored brand tokens + A-pattern (do not edit; see its README)
- `src/css/custom.css` — brand → Infima mapping layer and site chrome
- `static/img/brand/` — logo cuts, favicons, og cards

## Contributing

- DCO sign-off required (`git commit -s`); Conventional Commit subjects
- Branch before opening a PR; never commit directly to `main`
- See the repo-root [CONTRIBUTING.md](../CONTRIBUTING.md) for the full workflow
- Read `AGENTS.md` for documentation conventions, brand rules, and the
  renaming rules (which identifiers must never be renamed)
