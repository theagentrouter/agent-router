import React from 'react';
import Link from '@docusaurus/Link';
import { CalendarDays, Mic } from 'lucide-react';
import { DiscordIcon, GithubIcon } from '@site/src/components/home/BrandIcons';
import { community, type CommunityIcon } from '@site/src/data/home';
import styles from './styles.module.css';

const ICONS: Record<CommunityIcon, React.ComponentType<{ size?: number }>> = {
  chat: DiscordIcon,
  calendar: CalendarDays,
  github: GithubIcon,
  mic: Mic,
};

export default function CommunityPills(): React.ReactElement {
  return (
    <div className={styles.row}>
      {community.pills.map((pill) => {
        const Icon = ICONS[pill.icon];
        return (
          <Link key={pill.label} to={pill.to} className={styles.pill}>
            <Icon size={16} />
            {pill.label}
          </Link>
        );
      })}
    </div>
  );
}
