/**
 * Brand OG template for blog posts — 1200x630.
 * Design: design/mockups/b2-warm-minimal-pop.html (blog card SVGs) +
 * the faded A-pattern treatment from the homepage hero.
 * Ground + accent rotate by the post's first tag; edit CATEGORY_STYLES
 * to add categories. All colours come from the brand kit (tokens.css).
 */

import { fileURLToPath } from 'node:url';
import path from 'node:path';
import fs from 'node:fs';

const SITE_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const fontUrl = (f) => 'file://' + path.join(SITE_ROOT, 'static', 'fonts', f);
const markUrl = 'file://' + path.join(SITE_ROOT, 'static', 'img', 'brand', 'ar-mark-marquee.svg');

// The A-pattern tile, taken from the vendored brand CSS so there is a
// single source of truth (data-URI in --ar-pattern-airy).
const patternCss = fs.readFileSync(path.join(SITE_ROOT, 'src', 'css', 'brand', 'ar-pattern.css'), 'utf-8');
const PATTERN_AIRY = patternCss.match(/--ar-pattern-airy:\s*url\("([^"]+)"\)/)[1];

// ground + accent per category (first tag wins; cream-safe accent steps only)
// Dark grounds (ink / velvet / teal rotation) so cards pop on the site's
// light sections and in social feeds; accents are the kit's dark-mode steps.
export const CATEGORY_STYLES = {
  announcements: { ground: '#12100E', accent: '#FF7A33' },
  releases:      { ground: '#2A1C27', accent: '#EBCB8B' },
  features:      { ground: '#0B3B33', accent: '#FFA066' },
  news:          { ground: '#12100E', accent: '#31C4AA' },
  observability: { ground: '#2A1C27', accent: '#31C4AA' },
  architecture:  { ground: '#12100E', accent: '#A3ACD4' },
  reference:     { ground: '#2A1C27', accent: '#A3ACD4' },
  presentations: { ground: '#0B3B33', accent: '#EBCB8B' },
  adopters:      { ground: '#2A1C27', accent: '#D06E6D' },
  community:     { ground: '#0B3B33', accent: '#D06E6D' },
  default:       { ground: '#12100E', accent: '#FF7A33' },
};

export function ogHtml({ title, subtitle, tag, date }) {
  const style = CATEGORY_STYLES[(tag || 'default').toLowerCase()] ?? CATEGORY_STYLES.default;
  const titleSize = title.length > 56 ? 54 : title.length > 34 ? 62 : 72;
  const subtitleClamp = title.length > 56 ? 2 : 3;
  return `<!DOCTYPE html>
<html><head><meta charset="utf-8"><style>
  @font-face { font-family: 'Archivo'; src: url('${fontUrl('Archivo-Variable.woff2')}') format('woff2'); font-weight: 100 900; }
  @font-face { font-family: 'Inter'; src: url('${fontUrl('Inter-Variable.woff2')}') format('woff2'); font-weight: 100 900; }
  @font-face { font-family: 'JetBrains Mono'; src: url('${fontUrl('JetBrainsMono-Variable.woff2')}') format('woff2'); font-weight: 100 800; }
  * { margin: 0; box-sizing: border-box; }
  .card {
    width: 1200px; height: 630px; position: relative; overflow: hidden;
    background: ${style.ground};
    font-family: 'Inter', sans-serif; color: #F3EFEB;
    padding: 72px;
    isolation: isolate;
  }
  /* faded A-pattern: strongest top-right, fading out toward the text
     (kit rules: fade under text, opacity ceiling 0.10 on dark grounds) */
  .card::before {
    content: ''; position: absolute; inset: 0; z-index: -1; pointer-events: none;
    background-image: url("${PATTERN_AIRY}");
    background-size: auto 340px;
    background-position: top right;
    opacity: 0.10;
    -webkit-mask-image: linear-gradient(225deg, #000 20%, transparent 62%);
            mask-image: linear-gradient(225deg, #000 20%, transparent 62%);
  }
  .eyebrow {
    font: 600 30px 'Inter'; letter-spacing: 0.12em; text-transform: uppercase;
    color: ${style.accent};
  }
  .date {
    position: absolute; top: 76px; right: 72px;
    font: 500 28px 'JetBrains Mono'; color: #8A7F76;
  }
  .text {
    position: absolute; top: 168px; left: 72px; right: 180px;
  }
  .title {
    font: 800 ${titleSize}px/1.12 'Archivo'; letter-spacing: -0.02em;
    display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden;
  }
  .subtitle {
    margin-top: 26px; max-width: 880px;
    font: 400 31px/1.45 'Inter'; color: #B8AFA6;
    display: -webkit-box; -webkit-line-clamp: ${subtitleClamp}; -webkit-box-orient: vertical; overflow: hidden;
  }
  .site {
    position: absolute; bottom: 64px; left: 72px;
    font: 500 28px 'JetBrains Mono'; color: #B8AFA6;
  }
  .mark { position: absolute; bottom: 56px; right: 72px; width: 132px; }
  .oxidise {
    position: absolute; left: 0; right: 0; bottom: 0; height: 12px;
    background: linear-gradient(90deg, #FF5500 0%, #B83700 42%, #1A937F 100%);
  }
</style></head>
<body><div class="card">
  <div class="eyebrow">${escapeHtml(tag || 'blog')}</div>
  <div class="date">${escapeHtml(date || '')}</div>
  <div class="text">
    <div class="title">${escapeHtml(title)}</div>
    ${subtitle ? `<div class="subtitle">${escapeHtml(subtitle)}</div>` : ''}
  </div>
  <div class="site">theagentrouter.ai</div>
  <img class="mark" src="${markUrl}" alt="">
  <div class="oxidise"></div>
</div></body></html>`;
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
