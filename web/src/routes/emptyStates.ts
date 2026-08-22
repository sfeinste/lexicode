/*
 * The single empty-state inventory (S38) — UI spec §8, transcribed verbatim. Every §8
 * surface renders its copy FROM this module, so the spec's table exists in exactly one
 * place and web/src/routes/__tests__/emptyStates.test.tsx can hold the whole app to it.
 *
 * §8's detected-content variants are the two label builders at the bottom: the board's
 * "Import 12 open issues" (the count comes from the bootstrap preview's not-yet-imported
 * issues, fetched only while the board is empty and a repo is connected) and the wiki's
 * "Import AGENTS.md (detected)" (from the S35 import preview's detected files).
 */

export interface EmptyStateCopy {
  headline: string;
  body: string;
  /** The §8 primary CTA's label. Absent = the surface has no CTA (triage). */
  primary?: string;
  secondary?: string;
}

export const EMPTY_STATES = {
  /** Home, no projects. */
  noProjects: {
    headline: "Nothing here yet",
    body: "A project connects a repo, a board, and a roster of agents.",
    primary: "Create project",
  },
  /** A new project before a repo is connected (the single-gate connect card). */
  newProject: {
    headline: "Connect a repository to get started",
    body: "We'll import your issues, docs, and agent instructions automatically.",
    primary: "Connect GitHub repo",
  },
  /** Board with no tickets. The primary is the detected-content variant when the repo
   * scan finds open issues (boardImportLabel); "New ticket" otherwise. */
  board: {
    headline: "No tickets yet",
    body: "Import from GitHub Issues, or write one — press `C`.",
    secondary: "New ticket",
  },
  /** Wiki with no pages. The primary is the detected-content variant when AGENTS.md is
   * found (wikiImportLabel). */
  wiki: {
    headline: "Your project has no docs yet",
    body: "Docs here steer your agents, not just your teammates.",
    secondary: "New page",
  },
  agents: {
    headline: "No agents yet",
    body: "An agent is a name, a prompt, and a set of permissions.",
    primary: "Add an agent",
    secondary: "Use a starter roster",
  },
  runs: {
    headline: "No runs yet",
    body: "Delegate a ticket to an agent and its run appears here.",
    primary: "Go to board",
  },
  /** §8 specifies only the headline and the CTA here — the "body" cell is a stage
   * direction ("with removable filter chips"), so the sentence below is ours. */
  runsFiltered: {
    headline: "No runs match these filters",
    body: "Every chip above is removable — clear them to see the project's runs.",
    primary: "Clear filters",
  },
  triggers: {
    headline: "No triggers yet",
    body: "Start an agent automatically when something happens in the repo.",
    primary: "Add trigger",
  },
  triage: {
    headline: "Nothing to triage",
    body: "Tickets created by triggers and agents land here first.",
  },
} as const satisfies Record<string, EmptyStateCopy>;

/** §8's board detected-content variant: *"Import 12 open issues"*. */
export function boardImportLabel(openIssues: number): string {
  return `Import ${openIssues} open issues`;
}

/** §8's wiki detected-content variant: *"Import AGENTS.md (detected)"*; the plain import
 * label when the scan found docs but no AGENTS.md. */
export function wikiImportLabel(agentsMdDetected: boolean): string {
  return agentsMdDetected ? "Import AGENTS.md (detected)" : "Import from repository";
}
