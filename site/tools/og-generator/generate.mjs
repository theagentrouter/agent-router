/**
 * Generate brand OG images for blog posts.
 *
 *   npm run generate                # posts missing `image:` frontmatter
 *   npm run generate -- --all       # every post
 *   npm run generate -- --slug foo  # one post by slug
 *
 * Writes static/img/og/blog/<slug>.png (1200x630) and prints the
 * frontmatter line to add. Uses the system Chrome via playwright-core
 * (no browser download).
 */

import { fileURLToPath } from 'node:url';
import path from 'node:path';
import fs from 'node:fs';
import { glob } from 'glob';
import matter from 'gray-matter';
import { chromium } from 'playwright-core';
import { ogHtml } from './template.mjs';

const SITE_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const BLOG_DIR = path.join(SITE_ROOT, 'blog');
const OUT_DIR = path.join(SITE_ROOT, 'static', 'img', 'og', 'blog');

const args = process.argv.slice(2);
const all = args.includes('--all');
const slugArg = args.includes('--slug') ? args[args.indexOf('--slug') + 1] : null;

const files = await glob('**/*.{md,mdx}', { cwd: BLOG_DIR });
const posts = files
  .map((file) => {
    const { data } = matter(fs.readFileSync(path.join(BLOG_DIR, file), 'utf-8'));
    if (!data.title || !data.slug) return null;
    const date = file.match(/(\d{4}-\d{2}-\d{2})/)?.[1] ?? '';
    return {
      file,
      slug: data.slug,
      title: data.title,
      // subtitle: og_subtitle frontmatter wins, else the description
      subtitle: data.og_subtitle ?? data.description ?? '',
      tag: data.tags?.[0] ?? '',
      date,
      image: data.image,
    };
  })
  .filter(Boolean)
  .filter((p) => (slugArg ? p.slug === slugArg : all ? true : !p.image));

if (posts.length === 0) {
  console.log('Nothing to generate.');
  process.exit(0);
}

fs.mkdirSync(OUT_DIR, { recursive: true });
const browser = await chromium.launch({ channel: 'chrome' });
const page = await browser.newPage({ viewport: { width: 1200, height: 630 } });

// The page must itself be a file:// document so file:// fonts and images load.
const tmpHtml = path.join(OUT_DIR, '.og-tmp.html');
try {
  for (const post of posts) {
    fs.writeFileSync(tmpHtml, ogHtml(post));
    await page.goto('file://' + tmpHtml, { waitUntil: 'networkidle' });
    await page.evaluate(() => document.fonts.ready);
    const out = path.join(OUT_DIR, `${post.slug}.png`);
    await page.screenshot({ path: out });
    console.log(`${post.slug}.png  <-  ${post.file}`);
    console.log(`   add to frontmatter: image: /img/og/blog/${post.slug}.png`);
  }
} finally {
  fs.rmSync(tmpHtml, { force: true });
  await browser.close();
}
