# Adding a Supported Provider

Supported providers are listed in four places, and a provider PR must update
all of them. The homepage counts the entries in `providers.ts` ("Route to N
providers out of the box"), so a provider added only to the docs leaves the
homepage count out of date.

| #   | What                  | File                                                             |
| --- | --------------------- | ---------------------------------------------------------------- |
| 1   | Docs table            | `site/docs/capabilities/llm-integrations/supported-providers.md` |
| 2   | Logo                  | `site/static/img/providers/<slug>.svg` (this directory)          |
| 3   | Homepage logo strip   | `site/src/data/home/providers.ts`                                |
| 4   | Repo README logo grid | `README.md` (repo root), "Supported AI Providers" table          |

## 1. Docs table

Add a row to `site/docs/capabilities/llm-integrations/supported-providers.md`
with the provider's API docs link, the `AIServiceBackend` schema config, the
`BackendSecurityPolicy` auth type, and a status.

The homepage links to this page at `/docs/`, which serves the **latest
released version**, not `docs/`. If the provider already works on that
release with no code changes (for example an OpenAI-compatible provider that
only needs `{"name":"OpenAI","prefix":"..."}`), add the same row to
`site/versioned_docs/version-<latest>/capabilities/llm-integrations/supported-providers.md`.
The latest version is the first entry in `site/versions.json`. If the
provider needs a code change, add it to `docs/` only; it appears on `/docs/`
once the next version is cut.

Optional, but recommended:

- A connection guide at `site/docs/getting-started/connect-providers/<slug>.md`,
  linked from both lists in that directory's `index.md`.
- A runnable example at `examples/basic/<slug>.yaml`, listed in
  `examples/basic/README.md`.

## 2. Logo

Save an SVG of the provider's **official** mark in this directory.

- **Name**: lowercase and dashes, for example `azure-openai.svg` or `openai.svg`.
  Short forms are fine for long names (`tars.svg`).
- **Source**: most logos here come from the
  [LobeHub icon set](https://github.com/lobehub/lobe-icons) (npm package
  `@lobehub/icons-static-svg`). Use it when it has the provider, otherwise the
  provider's own brand or press kit. Never redraw or approximate a logo. If no
  official SVG exists, the provider can still go in the docs table, but stays
  off the homepage strip and README grid until someone supplies one.
- **Shape**: a square icon mark with a `0 0 24 24` viewBox works best. The
  strip renders every logo 28px tall, so a wide wordmark becomes unreadably
  small.
- **Format**: SVG only, transparent background, no embedded raster images.
- **Check both themes**: the strip shows logos in grayscale and inverts them
  in dark mode. A white or very light mark disappears on the light background,
  and colors of similar brightness merge into one gray. Look at it in both
  themes before opening the PR.

## 3. Homepage logo strip

Add an entry to `site/src/data/home/providers.ts`, keeping the list
alphabetical:

```ts
{ name: 'Example AI', logoUrl: '/img/providers/example-ai.svg', status: 'supported' },
```

`name` is the hover title and alt text, so use the provider's short brand name.

## 4. Repo README logo grid

Add a cell to the "Supported AI Providers" table in the repo-root `README.md`,
five cells per row:

```html
<td align="center" width="120">
  <img
    src="site/static/img/providers/example-ai.svg"
    width="60"
    height="60"
    alt="Example AI"
  />
  <br /><sub><b>Example AI</b></sub>
</td>
```

## Verify

From `site/`:

```shell
npm run start # check the homepage strip in light and dark mode
npm run build # broken links fail the build
```
