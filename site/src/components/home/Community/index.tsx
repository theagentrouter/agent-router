import React from 'react';
import SectionHeader from '@site/src/components/home/SectionHeader';
import CommunityPills from '@site/src/components/home/CommunityPills';
import styles from './styles.module.css';

export default function Community(): React.ReactElement {
  return (
    <section id="community" className={styles.section}>
      <div className="container">
        <SectionHeader label="Community" accent="verdigris" title="Built in the open">
          Weekly meetings, public design proposals, and maintainers from four companies. Showing up with a production problem is a valuable contribution.
        </SectionHeader>
        <CommunityPills />
      </div>
    </section>
  );
}
