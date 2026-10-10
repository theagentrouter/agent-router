import React from 'react';
import ThemedImage from '@theme/ThemedImage';
import SectionHeader from '@site/src/components/home/SectionHeader';
import { howItFits } from '@site/src/data/home';
import styles from './styles.module.css';

export default function HowItFits(): React.ReactElement {
  return (
    <section className={styles.section}>
      <div className="container">
        <SectionHeader label={howItFits.label} accent="spotlight" title={howItFits.title}>
          {howItFits.standfirst}
        </SectionHeader>
        <div className={styles.rel}>
          {howItFits.planes.map((plane) => (
            <div key={plane.name} className={styles.row}>
              <ThemedImage
                alt={`${plane.name} mark`}
                sources={{ light: plane.image.light, dark: plane.image.dark }}
                className={styles.planeImage}
              />
              <div>
                <div className={styles.planeName}>{plane.name}</div>
                <div className={styles.planeRole}>{plane.role}</div>
              </div>
            </div>
          ))}
          <div className={styles.refrain}>
            <b>{howItFits.refrainStrong}</b> {howItFits.refrain}
          </div>
        </div>
      </div>
    </section>
  );
}
