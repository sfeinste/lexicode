/*
 * S24 intervention surfaces on the run detail (UI spec §5.7):
 *
 * - The steering composer — live from `queued` onward (§10.3). A sent message renders
 *   inline with the "Applied after the current step." chip and flips to delivered when the
 *   adapter accepts it (run.message SSE frame → detail refetch).
 * - Stop — inline confirm, then terminal `canceled` with the artifact push preserved.
 * - Take over — a note field ("tell the agent what you changed before resuming"), then the
 *   copy-paste checkout block, monospace with a copy button (§10.7). The block also renders
 *   statically on any run whose state_reason is `takeover`, so it survives a reload.
 */
import { useState } from "react";

import type { Run, RunMessage } from "../../../lib/api/client";
import { useSteerRun, useStopRun, useTakeoverRun } from "../../../lib/api/runQueries";
import styles from "./runs.module.css";

const TERMINAL = new Set(["completed", "failed", "timed_out", "canceled", "loop_stopped"]);

export function InterventionBar({ run, messages }: { run: Run; messages: RunMessage[] }) {
  const terminal = TERMINAL.has(run.state);
  const steer = useSteerRun(run.id);
  const stop = useStopRun(run.id);
  const takeover = useTakeoverRun(run.id);

  const [draft, setDraft] = useState("");
  const [confirmingStop, setConfirmingStop] = useState(false);
  const [takingOver, setTakingOver] = useState(false);
  const [checkout, setCheckout] = useState<string | null>(null);

  const send = () => {
    const body = draft.trim();
    if (body === "" || steer.isPending) return;
    steer.mutate(body, { onSuccess: () => setDraft("") });
  };

  // The checkout block: the takeover response's copy, or — after a reload — derived from
  // the taken-over run itself.
  const staticCheckout =
    run.state_reason === "takeover" && run.branch !== null
      ? `git fetch origin && git checkout ${run.branch}`
      : null;
  const shownCheckout = checkout ?? staticCheckout;

  return (
    <>
      {shownCheckout !== null && (
        <div className={styles.takeoverBlock}>
          <div className={styles.takeoverLead}>
            Taken over. Check the branch out locally:
          </div>
          <CopyLine text={shownCheckout} />
          {run.takeover_note !== "" && (
            <div className={styles.takeoverNote}>Note for the next run: {run.takeover_note}</div>
          )}
        </div>
      )}

      {messages.length > 0 && (
        <div className={styles.steerList} aria-label="Steering messages">
          {messages.map((m) => (
            <div key={m.id} className={styles.steerRow} data-state={m.state}>
              <span className={styles.steerBody}>{m.body}</span>
              <span className={styles.steerState}>
                {m.state === "queued"
                  ? "Applied after the current step."
                  : m.state === "delivered"
                    ? "Delivered"
                    : "Dropped"}
              </span>
            </div>
          ))}
        </div>
      )}

      {!terminal && (
        <footer className={styles.steeringBar}>
          <input
            className={styles.steeringInput}
            placeholder="Send a message to this run… (applied after the current step)"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") send();
            }}
          />
          <button
            type="button"
            className={styles.steeringButton}
            disabled={steer.isPending || draft.trim() === ""}
            onClick={send}
          >
            Send
          </button>
          {confirmingStop ? (
            <>
              <button
                type="button"
                className={styles.steeringButton}
                data-danger="true"
                disabled={stop.isPending}
                onClick={() =>
                  stop.mutate("stopped by a human", {
                    onSettled: () => setConfirmingStop(false),
                  })
                }
              >
                Confirm stop
              </button>
              <button
                type="button"
                className={styles.steeringButton}
                onClick={() => setConfirmingStop(false)}
              >
                Keep running
              </button>
            </>
          ) : (
            <button
              type="button"
              className={styles.steeringButton}
              onClick={() => setConfirmingStop(true)}
            >
              Stop
            </button>
          )}
          <button
            type="button"
            className={styles.steeringButton}
            onClick={() => setTakingOver(true)}
          >
            Take over
          </button>
        </footer>
      )}

      {takingOver && (
        <TakeoverDialog
          pending={takeover.isPending}
          onCancel={() => setTakingOver(false)}
          onConfirm={(note) =>
            takeover.mutate(note, {
              onSuccess: (res) => {
                setCheckout(res.checkout === "" ? null : res.checkout);
                setTakingOver(false);
              },
            })
          }
        />
      )}
    </>
  );
}

function TakeoverDialog({
  pending,
  onCancel,
  onConfirm,
}: {
  pending: boolean;
  onCancel: () => void;
  onConfirm: (note: string) => void;
}) {
  const [note, setNote] = useState("");
  return (
    <div className={styles.takeoverDialogBackdrop} role="presentation" onClick={onCancel}>
      <div
        role="dialog"
        aria-label="Take over this run"
        className={styles.takeoverDialog}
        onClick={(e) => e.stopPropagation()}
      >
        <h2 className={styles.takeoverDialogTitle}>Take over this run</h2>
        <p className={styles.takeoverDialogBody}>
          The run stops (its branch is preserved) and you get a command to check the branch
          out locally.
        </p>
        <label className={styles.takeoverLabel}>
          Tell the agent what you changed before resuming
          <textarea
            className={styles.takeoverTextarea}
            value={note}
            onChange={(e) => setNote(e.target.value)}
            rows={3}
            placeholder="e.g. I renamed the retry helper and fixed the config loader myself."
          />
        </label>
        <div className={styles.takeoverDialogActions}>
          <button type="button" className={styles.steeringButton} onClick={onCancel}>
            Cancel
          </button>
          <button
            type="button"
            className={styles.steeringButton}
            data-danger="true"
            disabled={pending}
            onClick={() => onConfirm(note)}
          >
            Stop and take over
          </button>
        </div>
      </div>
    </div>
  );
}

/** A monospace one-liner with a copy button — the §10.7 checkout block. */
export function CopyLine({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className={styles.copyLine}>
      <code className={styles.copyLineText}>{text}</code>
      <button
        type="button"
        className={styles.copyButton}
        onClick={() => {
          void navigator.clipboard.writeText(text).then(() => {
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          });
        }}
      >
        {copied ? "Copied" : "Copy"}
      </button>
    </div>
  );
}
