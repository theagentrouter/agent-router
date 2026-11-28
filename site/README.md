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

- `docs/` — current ("Next") docs; `versioned_docs/` + `versions.json` — released versions (the latest, first in `versions.json`, renders at `/docs/`)
- `blog/` — posts under `blog/YYYY/`; authors in `blog/authors.yml`
- `src/data/home/` — **all homepage copy** (hero, capabilities, quickstart, community, providers)
- `static/img/providers/` — provider logos; its README is the checklist for adding a supported provider
- `src/components/home/` — homepage section components (one directory per section, CSS modules)
- `src/data/` — adopters, solutions, talks, release-notes data (JSON)
- `src/css/brand/` — vendored brand tokens + A-pattern (do not edit; see its README)
- `src/css/custom.css` — brand → Infima mapping layer and site chrome
- `static/img/brand/` — logo cuts, favicons, og cards

## Diagrams

New and edited diagrams have to read in both light and dark mode.

- **Mermaid**: the site recolours flowchart lines for dark mode. If you give a
  node a `fill`, give it a `color` too (for example
  `style A fill:#f9f,color:#12100E`), and add `accTitle` and `accDescr` for
  screen readers.
- **draw.io**: keep the source in `static/diagrams/` and export a light and a
  dark PNG with a transparent background (draw.io desktop CLI, tested with
  32.6.10):

  ```
  drawio -x -f png -s 2 -b 10 -t --theme light -o static/img/name.drawio.png static/diagrams/name.drawio
  drawio -x -f png -s 2 -b 10 -t --theme dark -o static/img/name-dark.drawio.png static/diagrams/name.drawio
  ```

  Docs pages use `static/img/` and show the pair with `ThemedImage`:

  ```mdx
  import ThemedImage from "@theme/ThemedImage";

  <ThemedImage
    alt="What the diagram shows"
    sources={{
      light: "/img/name.drawio.png",
      dark: "/img/name-dark.drawio.png",
    }}
  />
  ```

  The repo README keeps its pair in `blog/images/` and switches with
  `<picture>` and `prefers-color-scheme`. On Linux, install Liberation Sans so
  Helvetica labels keep their width.

## Contributing

- DCO sign-off required (`git commit -s`); Conventional Commit subjects
- Branch before opening a PR; never commit directly to `main`
- See the repo-root [CONTRIBUTING.md](../CONTRIBUTING.md) for the full workflow
- Read `AGENTS.md` for documentation conventions, brand rules, and the
  renaming rules (which identifiers must never be renamed)
