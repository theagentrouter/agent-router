import React from 'react';
import Link from '@docusaurus/Link';
import useBrokenLinks from '@docusaurus/useBrokenLinks';
import { ArrowUpRight } from 'lucide-react';
import SectionHeader from '@site/src/components/home/SectionHeader';
import { solutions as copy } from '@site/src/data/home';
import { solutions, type Solution } from '@site/src/data/solutions';
import styles from './styles.module.css';

function SolutionCard({ name, vendor, description, url, logoUrl, deployment }: Solution) {
  return (
    <a
      href={url}
      target="_blank"
      rel="noopener noreferrer"
      className={styles.card}
      aria-label={`${name} by ${vendor}: ${description}`}
    >
      <div className={styles.cardTop}>
        {logoUrl ? (
          <img src={logoUrl} alt={`${vendor} logo`} className={styles.logo} loading="lazy" />
        ) : (
          <span className={styles.vendor}>{vendor}</span>
        )}
        {deployment && <span className={styles.badge}>{deployment}</span>}
      </div>
      <h3 className={styles.cardTitle}>{name}</h3>
      <p className={styles.cardBody}>{description}</p>
      <span className={styles.cardLink}>
        {copy.cardLinkLabel}
        <ArrowUpRight size={14} strokeWidth={2.25} aria-hidden="true" />
      </span>
    </a>
  );
}

export default function Solutions(): React.ReactElement {
  // the navbar and hero link to /#solutions; register the anchor so the
  // build's broken-anchor check knows it exists
  useBrokenLinks().collectAnchor('solutions');
  return (
    <section id="solutions" className={styles.section}>
      <div className="container">
        <SectionHeader label={copy.label} accent="verdigris" title={copy.title}>
          {copy.standfirst}
        </SectionHeader>
        <div className={styles.grid}>
          {solutions.map((solution) => (
            <SolutionCard key={solution.name} {...solution} />
          ))}
        </div>
        <p className={styles.ctaLine}>
          {copy.ctaText} <Link to={copy.ctaLink.to}>{copy.ctaLink.label}</Link>
        </p>
      </div>
    </section>
  );
}
