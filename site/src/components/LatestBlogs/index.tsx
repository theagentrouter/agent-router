import React from 'react';
import Heading from '@theme/Heading';
import Link from '@docusaurus/Link';
import SectionHeader from '@site/src/components/home/SectionHeader';
import { useLatestBlogs } from '@site/src/hooks/useLatestBlogs';
import styles from './styles.module.css';

function BlogCard({
  title,
  description,
  image,
  tags,
  permalink,
}: {
  title: string;
  description: string;
  image: string;
  tags: string[];
  permalink: string;
}) {
  return (
    <Link to={permalink} className={styles.card}>
      {image ? (
        <div className={styles.imageContainer}>
          <img src={image} alt="" className={styles.image} loading="lazy" />
        </div>
      ) : (
        <div className={styles.imagePlaceholder} aria-hidden="true">
          <img src="/img/brand/ar-mark-marquee.svg" alt="" />
        </div>
      )}
      <div className={styles.body}>
        {tags.length > 0 && (
          <span className={styles.meta}>{tags.slice(0, 2).join(' · ').toUpperCase()}</span>
        )}
        <Heading as="h3" className={styles.title}>
          {title}
        </Heading>
        {description && <p className={styles.description}>{description}</p>}
      </div>
    </Link>
  );
}

export default function LatestBlogs(): React.ReactElement | null {
  const latestPosts = useLatestBlogs();

  if (latestPosts.length === 0) {
    return null;
  }

  return (
    <section className={styles.section}>
      <div className="container">
        <SectionHeader label="Blog" accent="curtain" title="Latest from the project" />
        <div className={styles.grid}>
          {latestPosts.map((post) => (
            <BlogCard key={post.slug} {...post} />
          ))}
        </div>
        <div className={styles.cta}>
          <Link className="button button--secondary" to="/blog">
            View All Posts
          </Link>
        </div>
      </div>
    </section>
  );
}
