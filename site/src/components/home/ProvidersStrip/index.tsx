import React from 'react';
import { providers } from '@site/src/data/home/providers';
import styles from './styles.module.css';

export default function ProvidersStrip(): React.ReactElement {
  const supported = providers.filter((p) => p.status === 'supported');
  return (
    <section className={styles.section}>
      <div className="container">
        <span className={styles.label}>
          Route to {supported.length} providers out of the box
        </span>
        <div className={styles.strip}>
          {supported.map((p) => (
            <img
              key={p.name}
              src={p.logoUrl}
              alt={p.name}
              title={p.name}
              loading="lazy"
            />
          ))}
        </div>
      </div>
    </section>
  );
}
