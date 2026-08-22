/*
 * The agent roster (UI spec §5.8): cards with avatar/color, name, role line, model, autonomy,
 * runs this week, success rate, spend, and an enable toggle. Empty state per §8 with the two
 * canonical actions: create one agent, or take the starter roster (the same Dev + Reviewer
 * pair the repository bootstrap suggests — shared server code, so they cannot drift).
 */
import { Link, useParams } from "@tanstack/react-router";
import { useState } from "react";

import { CostChip } from "../../../components/CostChip/CostChip";
import { EmptyState } from "../../../components/EmptyState/EmptyState";
import { ApiProblem, type Agent } from "../../../lib/api/client";
import {
  useAgentsQuery,
  useCreateAgent,
  useStarterRoster,
  useUpdateAgent,
} from "../../../lib/api/agentQueries";
import styles from "./agents.module.css";
import { AUTONOMY_STOPS } from "./constants";

const AUTONOMY_NAMES = Object.fromEntries(AUTONOMY_STOPS.map((s) => [s.value, s.name]));

export function AgentsPage() {
  const { key } = useParams({ from: "/shell/p/$key/agents/" });
  const roster = useAgentsQuery(key);
  const starter = useStarterRoster(key);
  const create = useCreateAgent(key);
  const update = useUpdateAgent(key);

  const [showCreate, setShowCreate] = useState(false);
  const [name, setName] = useState("");
  const [role, setRole] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [nameError, setNameError] = useState<string | null>(null);

  if (roster.isPending) return <div className={styles.page}>Loading…</div>;
  if (roster.isError) {
    return <div className={styles.page}>The roster failed to load.</div>;
  }

  const agents = roster.data.agents.filter((a) => a.archived_at === null);
  const archived = roster.data.agents.length - agents.length;

  const onMutationError = (err: unknown) => {
    setError(err instanceof ApiProblem ? err.detail || err.title : "The change did not save.");
  };

  const submitCreate = () => {
    setNameError(null);
    setError(null);
    create.mutate(
      { name, role },
      {
        onSuccess: () => {
          setShowCreate(false);
          setName("");
          setRole("");
        },
        onError: (err) => {
          if (err instanceof ApiProblem && err.errors?.some((f) => f.field === "name")) {
            setNameError(err.errors.find((f) => f.field === "name")?.message ?? err.title);
          } else {
            onMutationError(err);
          }
        },
      },
    );
  };

  const createForm = showCreate && (
    <form
      className={styles.createForm}
      onSubmit={(e) => {
        e.preventDefault();
        submitCreate();
      }}
    >
      <label className={styles.field}>
        Name
        <input autoFocus value={name} onChange={(e) => setName(e.target.value)} required />
        {nameError !== null && <span className={styles.fieldError}>{nameError}</span>}
      </label>
      <label className={styles.field}>
        Role
        <input value={role} onChange={(e) => setRole(e.target.value)} placeholder="Implementation" />
      </label>
      <button type="submit" disabled={create.isPending}>
        Create
      </button>
      <button type="button" onClick={() => setShowCreate(false)}>
        Cancel
      </button>
    </form>
  );

  if (agents.length === 0) {
    return (
      <div className={styles.page}>
        {error !== null && <p className={styles.error}>{error}</p>}
        {createForm}
        <EmptyState
          headline="No agents yet"
          body="An agent is a name, a prompt, and a set of permissions."
          primary={
            <button type="button" onClick={() => setShowCreate(true)}>
              Add an agent
            </button>
          }
          secondary={
            <button
              type="button"
              disabled={starter.isPending}
              onClick={() => starter.mutate(undefined, { onError: onMutationError })}
            >
              Use a starter roster
            </button>
          }
        />
      </div>
    );
  }

  return (
    <div className={styles.page}>
      <div className={styles.pageTitle}>
        <h1>Agents</h1>
        <div className={styles.titleActions}>
          <button type="button" onClick={() => setShowCreate(true)}>
            Create agent
          </button>
        </div>
      </div>
      {error !== null && <p className={styles.error}>{error}</p>}
      {createForm}
      <div className={styles.grid}>
        {agents.map((a) => (
          <AgentCard
            key={a.id}
            projectKey={key}
            agent={a}
            onToggle={(enabled) =>
              update.mutate({ id: a.id, body: { enabled } }, { onError: onMutationError })
            }
          />
        ))}
      </div>
      {archived > 0 && (
        <p className={styles.archivedNote}>
          {archived} archived agent{archived === 1 ? "" : "s"} — history kept.
        </p>
      )}
    </div>
  );
}

function AgentCard({
  projectKey,
  agent,
  onToggle,
}: {
  projectKey: string;
  agent: Agent;
  onToggle: (enabled: boolean) => void;
}) {
  return (
    <article className={styles.card} data-disabled={!agent.enabled} aria-label={agent.name}>
      <div className={styles.cardHead}>
        <span className={styles.avatar} style={{ background: agent.color }} aria-hidden="true">
          {agent.name.slice(0, 1).toUpperCase()}
        </span>
        <div>
          <Link
            to="/p/$key/agents/$id"
            params={{ key: projectKey, id: agent.id }}
            className={styles.cardName}
          >
            {agent.name}
          </Link>
          <div className={styles.cardRole}>{agent.role || " "}</div>
        </div>
      </div>
      <div className={styles.cardMeta}>
        <span className={styles.metaChip}>{agent.model}</span>
        <span className={styles.metaChip}>{AUTONOMY_NAMES[agent.autonomy]}</span>
      </div>
      <div className={styles.cardStats}>
        <span>
          <span className={styles.statValue}>{agent.runs_week}</span> runs this week
        </span>
        <span>
          <span className={styles.statValue}>
            {agent.success_rate === null ? "—" : `${Math.round(agent.success_rate * 100)}%`}
          </span>{" "}
          success
        </span>
        <span>
          <CostChip usd={agent.spend_week_cents === 0 ? 0 : agent.spend_week_cents / 100} /> spend
        </span>
      </div>
      <div className={styles.cardFoot}>
        <label className={styles.toggle}>
          <input
            type="checkbox"
            checked={agent.enabled}
            onChange={(e) => onToggle(e.target.checked)}
            aria-label={`${agent.name} enabled`}
          />
          {agent.enabled ? "Enabled" : "Disabled"}
        </label>
      </div>
    </article>
  );
}
