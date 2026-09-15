import solutionsData from './solutions.json';

export type Solution = {
  /** Product name as it should appear on the card. */
  name: string;
  /** Company or project that offers the solution. */
  vendor: string;
  /** One or two sentences: what it adds on top of Agent Router. */
  description: string;
  /** Product page (the whole card links here). */
  url: string;
  /** Optional logo (external URL or /img/solutions/<file>). */
  logoUrl?: string;
  /** Optional short badge, e.g. "Hosted", "Self-managed", "Managed service". */
  deployment?: string;
};

// Import solutions from the consolidated JSON file.
// Displayed in file order: append new entries at the end.
export const solutions: Solution[] = solutionsData as Solution[];
