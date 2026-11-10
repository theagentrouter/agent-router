import React from 'react';
import clsx from 'clsx';
import Heading from '@theme/Heading';
import styles from './styles.module.css';

export type SectionAccent =
  | 'marquee'
  | 'verdigris'
  | 'spotlight'
  | 'gilt'
  | 'curtain'
  | 'velvet';

/**
 * Shared centered section header: accent label, display title,
 * hairline underscore, optional standfirst paragraph.
 */
export default function SectionHeader({
  label,
  accent = 'marquee',
  title,
  children,
}: {
  label: string;
  accent?: SectionAccent;
  title: string;
  children?: React.ReactNode;
}): React.ReactElement {
  return (
    <div className={styles.head}>
      <span className={clsx(styles.label, styles[`accent_${accent}`])}>{label}</span>
      <Heading as="h2" className={styles.title}>
        {title}
      </Heading>
      <div className={styles.hairline} />
      {children && <p className={styles.standfirst}>{children}</p>}
    </div>
  );
}
