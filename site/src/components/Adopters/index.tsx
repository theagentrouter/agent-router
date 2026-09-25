import React from 'react';
import Link from '@docusaurus/Link';
import SectionHeader from '@site/src/components/home/SectionHeader';
import { community } from '@site/src/data/home';
import { sortedAdopters, type Adopter } from '@site/src/data/adopters';
import styles from './styles.module.css';

function AdopterLogo({ name, logoUrl, url, description }: Adopter) {
  const img = (
    <img
      src={logoUrl}
      alt={`${name} logo`}
      title={description ? `${name} — ${description}` : name}
      className={styles.logo}
      loading="lazy"
      onError={(e) => {
        const target = e.target as HTMLImageElement;
        target.src = '/img/adopters/placeholder-company.svg';
      }}
    />
  );

  if (url) {
    return (
      <a
        href={url}
        target="_blank"
        rel="noopener noreferrer"
        className={styles.logoLink}
        aria-label={`Visit ${name}${description ? `: ${description}` : ''}`}
      >
        {img}
      </a>
    );
  }
  return img;
}

export default function Adopters({
  children,
}: {
  children?: React.ReactNode;
}): React.ReactElement {
  return (
    <section id="adopters" className={styles.section}>
      <div className="container">
        <SectionHeader label="Adopters" accent="velvet" title="Adopted by" />
        <div className={styles.strip}>
          {sortedAdopters.map((adopter, idx) => (
            <AdopterLogo key={idx} {...adopter} />
          ))}
        </div>
        <p className={styles.ctaLine}>
          {community.ctaText}{' '}
          <Link to={community.ctaLink.to}>{community.ctaLink.label}</Link>
        </p>
        {children}
      </div>
    </section>
  );
}
