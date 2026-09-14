import React, { useState } from 'react';
import Link from '@docusaurus/Link';
import { quickstart } from '@site/src/data/home';
import styles from './styles.module.css';

function TerminalLine({ kind, text }: { kind: string; text?: string }): React.ReactElement {
  if (kind === 'blank') return <span>{'\n'}</span>;
  if (kind === 'comment') return <span className={styles.comment}>{text + '\n'}</span>;
  const raw = text ?? '';
  const trimmed = raw.trimStart();
  const indent = raw.slice(0, raw.length - trimmed.length);
  if (indent.length > 0) return <span>{raw + '\n'}</span>;
  // env assignments before the command word (VAR=... cmd) get their own colour
  const words = trimmed.split(' ');
  const cmdIdx = words.findIndex((w) => !w.includes('='));
  return (
    <span>
      {words.map((word, i) => {
        const chunk = i < words.length - 1 ? word + ' ' : word;
        if (i < cmdIdx) {
          return (
            <span key={i} className={styles.env}>
              {chunk}
            </span>
          );
        }
        if (i === cmdIdx) {
          return (
            <span key={i} className={styles.cmd}>
              {chunk}
            </span>
          );
        }
        return chunk;
      })}
      {'\n'}
    </span>
  );
}

export default function Quickstart(): React.ReactElement {
  const [activeId, setActiveId] = useState(quickstart.tabs[0].id);
  const active = quickstart.tabs.find((t) => t.id === activeId) ?? quickstart.tabs[0];
  return (
    <section id="quickstart" className={styles.section}>
      <div className="container">
        <div className={styles.panel}>
          <div>
            <span className={styles.label}>{quickstart.label}</span>
            <h2 className={styles.title}>{quickstart.title}</h2>
            <p className={styles.body}>{quickstart.body}</p>
            <Link className="button button--primary" to={active.cta.to}>
              {active.cta.label}
            </Link>
          </div>
          <div>
            <div className={styles.tabs} role="tablist" aria-label="Where to run Agent Router">
              {quickstart.tabs.map((tab) => (
                <button
                  key={tab.id}
                  type="button"
                  role="tab"
                  aria-selected={tab.id === active.id}
                  className={tab.id === active.id ? `${styles.tab} ${styles.tabActive}` : styles.tab}
                  onClick={() => setActiveId(tab.id)}
                >
                  {tab.label}
                </button>
              ))}
            </div>
            <div className={styles.term}>
              <div className={styles.termBar}>
                <i />
                <i />
                <i />
              </div>
              <pre className={styles.termPre}>
                {active.terminal.map((line, i) => (
                  <TerminalLine key={`${active.id}-${i}`} {...line} />
                ))}
              </pre>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
