import React from 'react';
import Layout from '@theme/Layout';
import Heading from '@theme/Heading';
import Link from '@docusaurus/Link';
import talksData from '../data/talks.json';
import styles from './talks.module.css';

type Talk = {
  date: string;
  title: string;
  url?: string;
  event?: string;
  speaker?: string;
  description?: string;
};

function formatDate(dateString: string): string {
  const months = ['January', 'February', 'March', 'April', 'May', 'June',
    'July', 'August', 'September', 'October', 'November', 'December'];
  const [year, month, day] = dateString.split('-').map(Number);
  return `${months[month - 1]} ${day}, ${year}`;
}

function TalkItem({ talk }: { talk: Talk }) {
  return (
    <li className={styles.talkItem}>
      <div className={styles.talkDate}>{formatDate(talk.date)}</div>
      <div className={styles.talkDetails}>
        {talk.url ? (
          <a href={talk.url} target="_blank" rel="noopener noreferrer" className={styles.talkTitle}>
            {talk.title}
          </a>
        ) : (
          <div className={styles.talkTitle}>{talk.title}</div>
        )}
        {talk.event && <div className={styles.talkEvent}>{talk.event}</div>}
        {talk.speaker && <div className={styles.talkSpeaker}>{talk.speaker}</div>}
        {talk.description && <div className={styles.talkDescription}>{talk.description}</div>}
      </div>
    </li>
  );
}

export default function Talks(): React.ReactElement {
  const talks = talksData as Talk[];
  const today = new Date();
  today.setHours(0, 0, 0, 0);

  // Sort talks by date descending
  const sortedTalks = [...talks].sort((a, b) =>
    new Date(b.date).getTime() - new Date(a.date).getTime()
  );

  // Split into upcoming and past
  const upcomingTalks = sortedTalks.filter(talk => new Date(talk.date) >= today);
  const pastTalks = sortedTalks.filter(talk => new Date(talk.date) < today);

  return (
    <Layout
      title="Talks and Presentations"
      description="Talks and presentations about Agent Router">
      <main className={styles.talksPage}>
        <div className="container">
          <div className={styles.header}>
            <Heading as="h1">Talks and Presentations</Heading>
            <p className={styles.description}>
              Watch talks and presentations about Agent Router from conferences, meetups, and community events.
            </p>
            <div className={styles.contributeButton}>
              <Link
                className="button button--primary button--lg"
                href="https://github.com/theagentrouter/agent-router/edit/main/site/src/data/talks.json"
                target="_blank"
                rel="noopener noreferrer">
                Add your session!
              </Link>
            </div>
          </div>

          <section className={styles.section}>
            <Heading as="h2" className={styles.sectionTitle}>Upcoming Talks</Heading>
            {upcomingTalks.length > 0 ? (
              <ul className={styles.talksList}>
                {upcomingTalks.map((talk, idx) => (
                  <TalkItem key={idx} talk={talk} />
                ))}
              </ul>
            ) : (
              <p className={styles.placeholder}>No upcoming talks scheduled yet.</p>
            )}
          </section>

          <section className={styles.section}>
            <Heading as="h2" className={styles.sectionTitle}>Past Talks</Heading>
            {pastTalks.length > 0 ? (
              <ul className={styles.talksList}>
                {pastTalks.map((talk, idx) => (
                  <TalkItem key={idx} talk={talk} />
                ))}
              </ul>
            ) : (
              <p className={styles.placeholder}>No past talks yet.</p>
            )}
          </section>
        </div>
      </main>
    </Layout>
  );
}
