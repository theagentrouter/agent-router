import type { LoadContext, Plugin } from '@docusaurus/types';
import * as fs from 'fs';
import * as path from 'path';
import matter from 'gray-matter';
import { glob } from 'glob';

export type BlogPostMeta = {
  title: string;
  description: string;
  image: string;
  tags: string[];
  slug: string;
  date: string;
  permalink: string;
};

export type LatestBlogsPluginData = {
  latestPosts: BlogPostMeta[];
};

export default function latestBlogsPlugin(context: LoadContext): Plugin {
  return {
    name: 'docusaurus-plugin-latest-blogs',

    async contentLoaded({ actions }) {
      const { setGlobalData } = actions;
      const blogDir = path.join(context.siteDir, 'blog');

      // Find all blog markdown files (md and mdx)
      const files = await glob('**/*.{md,mdx}', { cwd: blogDir });

      const posts: BlogPostMeta[] = files
        .map((file) => {
          try {
            const filePath = path.join(blogDir, file);
            const content = fs.readFileSync(filePath, 'utf-8');
            const { data } = matter(content);

            // Skip files without required frontmatter
            if (!data.title || !data.slug) {
              return null;
            }

            // Skip drafts — Docusaurus doesn't render them, so linking
            // to their permalink would break the build.
            if (data.draft === true) {
              return null;
            }

            // Extract date from filename (YYYY-MM-DD-slug.md)
            const dateMatch = file.match(/(\d{4}-\d{2}-\d{2})/);
            const date = dateMatch?.[1] || '';

            return {
              title: data.title,
              description: data.description || '',
              image: data.image || '',
              tags: data.tags || [],
              slug: data.slug,
              date,
              permalink: `/blog/${data.slug}`,
            };
          } catch {
            return null;
          }
        })
        .filter((post): post is BlogPostMeta => post !== null);

      // Sort by date descending, take top 3
      const latestPosts = posts
        .sort((a, b) => b.date.localeCompare(a.date))
        .slice(0, 3);

      setGlobalData({ latestPosts } as LatestBlogsPluginData);
    },
  };
}
