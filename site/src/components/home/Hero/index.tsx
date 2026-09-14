import React from 'react';
import Link from '@docusaurus/Link';
import { hero } from '@site/src/data/home';
import styles from './styles.module.css';

export default function Hero(): React.ReactElement {
  return (
    <header className={styles.hero}>
      <img className={styles.mark} src="/img/brand/ar-mark-marquee.svg" alt="" />
      <h1 className={styles.headline}>
        {hero.headline}
        <br />
        <span className={styles.accent}>{hero.headlineAccent}</span>
      </h1>
      <p className={styles.lead}>
        <b>{hero.leadStrong}</b> {hero.lead}
      </p>
      <div className={styles.ctas}>
        <Link className="button button--primary button--lg" to={hero.ctas[0].to}>
          {hero.ctas[0].label}
        </Link>
        <Link className="button button--secondary button--lg" to={hero.ctas[1].to}>
          {hero.ctas[1].label}
        </Link>
      </div>
      <p className={styles.sub}>{hero.sub}</p>
    </header>
  );
}
