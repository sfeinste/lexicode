/*
 * S38 acceptance: every empty state matches UI spec §8's copy EXACTLY.
 *
 * The fixtures below are a transcription of the §8 table (headline · body · primary ·
 * secondary), kept deliberately separate from the app's inventory (../emptyStates.ts).
 * The test holds the two to each other, renders each state through the real EmptyState
 * component to prove the copy survives to the DOM, and scans each §8 surface's source to
 * prove the page renders FROM the inventory rather than from a drifting literal.
 *
 * §8 cells that are stage directions, not copy, are handled as the spec means them:
 * - Runs (filtered): the body cell is "*with removable filter chips*" — a behavior (the
 *   chips row is above the empty state), so only headline + CTA are pinned.
 * - Triggers: "(3 suggested, pre-filled, off)" annotates where suggested triggers come
 *   from (the bootstrap flow proposes pre-filled triggers with the toggles off); the CTA
 *   label itself is "Add trigger".
 * - Board/Wiki detected-content variants are the label builders, pinned verbatim here.
 */
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { EmptyState } from "../../components/EmptyState/EmptyState";
import { EMPTY_STATES, boardImportLabel, wikiImportLabel } from "../emptyStates";

const here = dirname(fileURLToPath(import.meta.url));
const routesDir = join(here, "..");

/** UI spec §8, transcribed. Do not edit without editing design/ui-ux-spec.md. */
const SPEC = {
  noProjects: {
    headline: "Nothing here yet",
    body: "A project connects a repo, a board, and a roster of agents.",
    primary: "Create project",
  },
  newProject: {
    headline: "Connect a repository to get started",
    body: "We'll import your issues, docs, and agent instructions automatically.",
    primary: "Connect GitHub repo",
  },
  board: {
    headline: "No tickets yet",
    body: "Import from GitHub Issues, or write one — press `C`.",
    primaryDetected: "Import 12 open issues",
    secondary: "New ticket",
  },
  wiki: {
    headline: "Your project has no docs yet",
    body: "Docs here steer your agents, not just your teammates.",
    primaryDetected: "Import AGENTS.md (detected)",
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
  runsFiltered: {
    headline: "No runs match these filters",
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
} as const;

describe("§8 empty-state copy, verbatim (S38)", () => {
  it("no projects", () => {
    expect(EMPTY_STATES.noProjects).toEqual(SPEC.noProjects);
  });
  it("new project (connect gate)", () => {
    expect(EMPTY_STATES.newProject).toEqual(SPEC.newProject);
  });
  it("board, with the detected-content variant", () => {
    expect(EMPTY_STATES.board.headline).toBe(SPEC.board.headline);
    expect(EMPTY_STATES.board.body).toBe(SPEC.board.body);
    expect(EMPTY_STATES.board.secondary).toBe(SPEC.board.secondary);
    expect(boardImportLabel(12)).toBe(SPEC.board.primaryDetected);
  });
  it("wiki, with the detected-content variant", () => {
    expect(EMPTY_STATES.wiki.headline).toBe(SPEC.wiki.headline);
    expect(EMPTY_STATES.wiki.body).toBe(SPEC.wiki.body);
    expect(EMPTY_STATES.wiki.secondary).toBe(SPEC.wiki.secondary);
    expect(wikiImportLabel(true)).toBe(SPEC.wiki.primaryDetected);
  });
  it("agents", () => {
    expect(EMPTY_STATES.agents).toEqual(SPEC.agents);
  });
  it("runs", () => {
    expect(EMPTY_STATES.runs).toEqual(SPEC.runs);
  });
  it("runs (filtered): headline and CTA — the body cell is a stage direction", () => {
    expect(EMPTY_STATES.runsFiltered.headline).toBe(SPEC.runsFiltered.headline);
    expect(EMPTY_STATES.runsFiltered.primary).toBe(SPEC.runsFiltered.primary);
  });
  it("triggers", () => {
    expect(EMPTY_STATES.triggers.headline).toBe(SPEC.triggers.headline);
    expect(EMPTY_STATES.triggers.body).toBe(SPEC.triggers.body);
    expect(EMPTY_STATES.triggers.primary).toBe(SPEC.triggers.primary);
  });
  it("triage — deliberately no CTA", () => {
    expect(EMPTY_STATES.triage).toEqual(SPEC.triage);
    expect("primary" in EMPTY_STATES.triage).toBe(false);
  });
});

describe("the copy survives to the DOM through EmptyState", () => {
  it.each(Object.entries(EMPTY_STATES))("%s renders headline and body", (_name, copy) => {
    const { getByRole, getByText } = render(
      <EmptyState
        headline={copy.headline}
        body={copy.body}
        primary={"primary" in copy ? <button>{copy.primary}</button> : undefined}
      />,
    );
    expect(getByRole("heading").textContent).toBe(copy.headline);
    expect(getByText(copy.body)).toBeTruthy();
  });
});

/** Each §8 surface must render from the inventory, not from a re-typed literal. */
const SURFACE_SOURCES: Array<[string, string, string]> = [
  ["no projects", "home/HomePage.tsx", "EMPTY_STATES.noProjects"],
  ["new project", "project/overview/ConnectRepoCard.tsx", "EMPTY_STATES.newProject"],
  ["board", "project/board/BoardPage.tsx", "EMPTY_STATES.board"],
  ["board import variant", "project/board/BoardPage.tsx", "boardImportLabel("],
  ["wiki", "project/wiki/WikiIndexPage.tsx", "EMPTY_STATES.wiki"],
  ["wiki import variant", "project/wiki/WikiIndexPage.tsx", "wikiImportLabel("],
  ["agents", "project/agents/AgentsPage.tsx", "EMPTY_STATES.agents"],
  ["runs", "project/runs/RunsPage.tsx", "EMPTY_STATES.runs"],
  ["runs filtered", "project/runs/RunsPage.tsx", "EMPTY_STATES.runsFiltered"],
  ["triggers", "project/triggers/TriggersPage.tsx", "EMPTY_STATES.triggers"],
  ["triage", "project/triage/TriagePage.tsx", "EMPTY_STATES.triage"],
];

describe("§8 surfaces render from the single inventory", () => {
  it.each(SURFACE_SOURCES)("%s (%s)", (_name, file, marker) => {
    const src = readFileSync(join(routesDir, file), "utf8");
    expect(src.includes(marker), `${file} should use ${marker}`).toBe(true);
  });
});
