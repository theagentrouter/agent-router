import React from 'react';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import Layout from '@theme/Layout';
import Hero from '@site/src/components/home/Hero';
import HowItFits from '@site/src/components/home/HowItFits';
import Capabilities from '@site/src/components/home/Capabilities';
import Quickstart from '@site/src/components/home/Quickstart';
import ProvidersStrip from '@site/src/components/home/ProvidersStrip';
import Community from '@site/src/components/home/Community';
import LatestBlogs from '@site/src/components/LatestBlogs';
import Adopters from '@site/src/components/Adopters';

/**
 * Homepage — "Warm Minimal + Pop" (see design/mockups/b2-warm-minimal-pop.html).
 * All copy lives in src/data/home/; sections are self-contained components.
 */
export default function Home(): React.ReactElement {
  const { siteConfig } = useDocusaurusContext();
  return (
    <Layout title={siteConfig.title} description={siteConfig.tagline}>
      <Hero />
      <main>
        {/* social proof first: provider breadth, then who runs it */}
        <ProvidersStrip />
        <Adopters />
        <HowItFits />
        <Capabilities />
        <Quickstart />
        <LatestBlogs />
        <Community />
      </main>
    </Layout>
  );
}
