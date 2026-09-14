import React from 'react';
import Link from '@docusaurus/Link';
import { Hexagon, ArrowRightLeft, Lock, Waypoints, BarChart3, Activity } from 'lucide-react';
import SectionHeader from '@site/src/components/home/SectionHeader';
import { capabilities, type CapabilityIcon } from '@site/src/data/home';
import styles from './styles.module.css';

const ICONS: Record<CapabilityIcon, React.ComponentType<{ size?: number; strokeWidth?: number }>> = {
  hexagon: Hexagon,
  lanes: ArrowRightLeft,
  lock: Lock,
  hub: Waypoints,
  bars: BarChart3,
  clock: Activity,
};

export default function Capabilities(): React.ReactElement {
  return (
    <section className={styles.section}>
      <div className="container">
        <SectionHeader label={capabilities.label} accent="gilt" title={capabilities.title}>
          {capabilities.standfirst}
        </SectionHeader>
        <div className={styles.grid}>
          {capabilities.items.map((item) => {
            const Icon = ICONS[item.icon];
            const card = (
              <>
                <div className={styles.icon}>
                  <Icon size={20} strokeWidth={2} />
                </div>
                <h3 className={styles.cardTitle}>{item.title}</h3>
                <p className={styles.cardBody}>{item.body}</p>
              </>
            );
            return item.to ? (
              <Link key={item.title} to={item.to} className={styles.card}>
                {card}
              </Link>
            ) : (
              <div key={item.title} className={styles.card}>
                {card}
              </div>
            );
          })}
        </div>
      </div>
    </section>
  );
}
