/*
 * The ⌘J ask-an-agent palette (UI spec §6, S38). §6 splits the two palettes deliberately:
 * ⌘K is deterministic actions; ⌘J is talking to an agent. This one lists the project's
 * delegate-eligible agents; picking one and typing a prompt requests a free-floating run
 * (D-15) via POST /projects/{key}/runs, then jumps to the created run's detail.
 *
 * Mounted in the shell next to the ⌘K palette; it registers its own mod+j binding, so the
 * cheatsheet and the ⌘K palette list it like any other command. Enabled only while a
 * project is open — an agent belongs to a project.
 */
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useMemo, useRef, useState } from "react";

import { useEligibleAgents } from "../../lib/api/agentQueries";
import type { Agent } from "../../lib/api/client";
import { useCreateRun } from "../../lib/api/runQueries";
import { fuzzyFilter } from "../../lib/fuzzy";
import { useKeyBindings, useKeyScope } from "../../lib/keyboard/hooks";
import { useUIStore } from "../../stores/ui";
import styles from "./AskAgentPalette.module.css";

export function AskAgentPalette({ projectKey }: { projectKey: string | undefined }) {
  const open = useUIStore((s) => s.askAgentOpen);
  const setOpen = useUIStore((s) => s.setAskAgentOpen);

  useKeyBindings(
    () => [
      {
        id: "shell.ask-agent",
        scope: "global",
        chord: "mod+j",
        title: "Ask an agent",
        group: "General",
        palette: true,
        enabled: () => projectKey !== undefined,
        run: () => {
          const s = useUIStore.getState();
          s.setAskAgentOpen(!s.askAgentOpen);
        },
      },
    ],
    [projectKey],
  );
  useKeyScope("modal", open && projectKey !== undefined);
  useKeyBindings(
    () =>
      open && projectKey !== undefined
        ? [
            {
              id: "ask-agent.close",
              scope: "modal",
              chord: "escape",
              title: "Close ask-an-agent",
              group: "General",
              run: () => setOpen(false),
            },
          ]
        : [],
    [open, projectKey, setOpen],
  );

  if (!open || projectKey === undefined) return null;
  return <AskAgentDialog projectKey={projectKey} onClose={() => setOpen(false)} />;
}

function AskAgentDialog({
  projectKey,
  onClose,
}: {
  projectKey: string;
  onClose: () => void;
}) {
  const navigate = useNavigate();
  const { agents } = useEligibleAgents(projectKey);
  const createRun = useCreateRun(projectKey);

  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0);
  const [picked, setPicked] = useState<Agent | null>(null);
  const [prompt, setPrompt] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const promptRef = useRef<HTMLTextAreaElement>(null);

  const shown = useMemo(
    () => fuzzyFilter(query, agents, (a) => a.name),
    [agents, query],
  );

  useEffect(() => {
    (picked === null ? inputRef.current : promptRef.current)?.focus();
  }, [picked]);
  useEffect(() => {
    setCursor(0);
  }, [query]);

  const submit = () => {
    if (picked === null || prompt.trim() === "" || createRun.isPending) return;
    createRun.mutate(
      { agent_id: picked.id, prompt },
      {
        onSuccess: (res) => {
          onClose();
          void navigate({
            to: "/p/$key/runs/$id",
            params: { key: projectKey, id: res.run_id },
          });
        },
      },
    );
  };

  return (
    <div className={styles.overlay} onClick={onClose}>
      <div
        role="dialog"
        aria-label="Ask an agent"
        className={styles.dialog}
        onClick={(e) => e.stopPropagation()}
      >
        {picked === null ? (
          <>
            <input
              ref={inputRef}
              className={styles.input}
              placeholder="Ask which agent…"
              aria-label="Filter agents"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "ArrowDown") {
                  e.preventDefault();
                  setCursor((c) => Math.min(c + 1, shown.length - 1));
                } else if (e.key === "ArrowUp") {
                  e.preventDefault();
                  setCursor((c) => Math.max(c - 1, 0));
                } else if (e.key === "Enter" && shown[cursor]) {
                  e.preventDefault();
                  setPicked(shown[cursor]);
                }
              }}
            />
            <ul className={styles.list} role="listbox" aria-label="Project agents">
              {shown.map((a, i) => (
                <li key={a.id}>
                  <button
                    type="button"
                    role="option"
                    aria-selected={i === cursor}
                    className={styles.item}
                    data-selected={i === cursor || undefined}
                    onMouseEnter={() => setCursor(i)}
                    onClick={() => setPicked(a)}
                  >
                    <span className={styles.agentDot} style={{ background: a.color }} />
                    <span className={styles.agentName}>{a.name}</span>
                    <span className={styles.agentRole}>{a.role}</span>
                  </button>
                </li>
              ))}
              {shown.length === 0 && (
                <li className={styles.none}>No agents — add one on the Agents tab.</li>
              )}
            </ul>
          </>
        ) : (
          <>
            <div className={styles.pickedRow}>
              <span className={styles.agentDot} style={{ background: picked.color }} />
              <span className={styles.agentName}>{picked.name}</span>
              <button
                type="button"
                className={styles.changeAgent}
                onClick={() => setPicked(null)}
              >
                Change agent
              </button>
            </div>
            <textarea
              ref={promptRef}
              className={styles.prompt}
              placeholder={`What should ${picked.name} do?`}
              aria-label="Prompt"
              rows={4}
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                  e.preventDefault();
                  submit();
                }
              }}
            />
            {createRun.isError && (
              <p role="alert" className={styles.error}>
                {createRun.error.message}
              </p>
            )}
            <div className={styles.footer}>
              <span className={styles.hint}>⌘⏎ to start the run</span>
              <button
                type="button"
                className={styles.run}
                disabled={prompt.trim() === "" || createRun.isPending}
                onClick={submit}
              >
                {createRun.isPending ? "Starting…" : "Start run"}
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
