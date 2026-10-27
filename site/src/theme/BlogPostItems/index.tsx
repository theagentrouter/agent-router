import React from 'react';
import clsx from 'clsx';
import Link from '@docusaurus/Link';
import Heading from '@theme/Heading';
import type {Props} from '@theme/BlogPostItems';
import styles from './styles.module.css';

/**
 * Swizzled (ejected): renders blog listings as an OG-card grid in the
 * homepage's card language instead of stacked post excerpts. The first
 * item on a page gets the wide "featured" treatment.
 */

function formatDate(date: string): string {
  return new Intl.DateTimeFormat('en-US', {
    month: 'short',
    day: 'numeric',
    year: 'numeric',
    timeZone: 'UTC',
  }).format(new Date(date));
}

export default function BlogPostItems({items}: Props): React.ReactElement {
  return (
    <div className={styles.grid}>
      {items.map(({content}, index) => {
        const {metadata} = content;
        const {permalink, title, description, date, readingTime, tags, authors} = metadata;
        const image = metadata.frontMatter.image as string | undefined;
        const featured = index === 0;
        return (
          <Link
            key={permalink}
            to={permalink}
            className={clsx(styles.card, featured && styles.featured)}>
            {image ? (
              <div className={styles.imageContainer}>
                <img src={image} alt="" className={styles.image} loading={featured ? 'eager' : 'lazy'} />
              </div>
            ) : (
              <div className={clsx(styles.imageContainer, styles.imagePlaceholder)} aria-hidden="true">
                <img src="/img/brand/ar-mark-marquee.svg" alt="" />
              </div>
            )}
            <div className={styles.body}>
              {tags.length > 0 && (
                <span className={styles.meta}>
                  {tags.slice(0, 2).map((tag) => tag.label).join(' · ').toUpperCase()}
                </span>
              )}
              <Heading as="h2" className={styles.title}>
                {title}
              </Heading>
              {description && <p className={styles.description}>{description}</p>}
              <div className={styles.foot}>
                {authors.length > 0 && (
                  <span className={styles.authors}>
                    {authors.slice(0, 3).map(
                      (author) =>
                        author.imageURL && (
                          <img
                            key={author.name ?? author.imageURL}
                            src={author.imageURL}
                            alt=""
                            className={styles.avatar}
                            loading="lazy"
                          />
                        ),
                    )}
                    <span className={styles.authorNames}>
                      {authors.length > 2
                        ? `${authors[0].name} +${authors.length - 1}`
                        : authors.map((a) => a.name).join(' & ')}
                    </span>
                  </span>
                )}
                <span className={styles.dateline}>
                  {formatDate(date)}
                  {readingTime ? ` · ${Math.ceil(readingTime)} min read` : ''}
                </span>
              </div>
            </div>
          </Link>
        );
      })}
    </div>
  );
}
