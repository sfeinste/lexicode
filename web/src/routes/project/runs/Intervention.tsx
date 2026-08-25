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
 *
 * D-1 (amended) — every control here is a Material UI component:
 *   TextField · Button · Chip · Alert · List · Dialog (+ Title/Content/Actions) · Paper.
 * The take-over dialog is the clearest win. It was a hand-rolled backdrop div with
 * role="dialog": no focus trap, no Escape, no restore of focus on close, and a click
 * handler on the backdrop that a keyboard user could never reach. MUI's Dialog does all
 * four by construction, which is the argument for a library in one screenshot.
 */
import { useState } from "react";
import Alert from "@mui/material/Alert";
import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import Chip from "@mui/material/Chip";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogContentText from "@mui/material/DialogContentText";
import DialogTitle from "@mui/material/DialogTitle";
import List from "@mui/material/List";
import ListItem from "@mui/material/ListItem";
import Paper from "@mui/material/Paper";
import TextField from "@mui/material/TextField";
import Typography from "@mui/material/Typography";

import type { Run, RunMessage } from "../../../lib/api/client";
import { useSteerRun, useStopRun, useTakeoverRun } from "../../../lib/api/runQueries";

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
        <Alert severity="info" icon={false} sx={{ mt: 1 }}>
          <Typography variant="body1" sx={{ mb: "6px" }}>
            Taken over. Check the branch out locally:
          </Typography>
          <CopyLine text={shownCheckout} />
          {run.takeover_note !== "" && (
            <Typography variant="body2" sx={{ mt: "6px", color: "text.secondary" }}>
              Note for the next run: {run.takeover_note}
            </Typography>
          )}
        </Alert>
      )}

      {messages.length > 0 && (
        <List dense aria-label="Steering messages" sx={{ py: 0 }}>
          {messages.map((m) => (
            <ListItem
              key={m.id}
              data-state={m.state}
              disableGutters
              sx={{ gap: 1, justifyContent: "space-between" }}
            >
              <Typography variant="body1">{m.body}</Typography>
              <Chip
                size="small"
                variant="outlined"
                label={
                  m.state === "queued"
                    ? "Applied after the current step."
                    : m.state === "delivered"
                      ? "Delivered"
                      : "Dropped"
                }
                sx={{ color: m.state === "dropped" ? "error.main" : "text.secondary" }}
              />
            </ListItem>
          ))}
        </List>
      )}

      {!terminal && (
        <Paper
          component="footer"
          variant="outlined"
          sx={{
            display: "flex",
            alignItems: "center",
            gap: 1,
            p: 1,
            mt: 1,
            backgroundColor: "lexicode.surface2",
          }}
        >
          <TextField
            size="small"
            fullWidth
            label="Message this run"
            placeholder="Send a message to this run…"
            helperText="Applied after the current step."
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") send();
            }}
          />
          <Button
            variant="contained"
            disabled={steer.isPending || draft.trim() === ""}
            onClick={send}
          >
            Send
          </Button>
          {confirmingStop ? (
            <>
              <Button
                variant="contained"
                color="error"
                disabled={stop.isPending}
                onClick={() =>
                  stop.mutate("stopped by a human", {
                    onSettled: () => setConfirmingStop(false),
                  })
                }
              >
                Confirm stop
              </Button>
              <Button variant="outlined" onClick={() => setConfirmingStop(false)}>
                Keep running
              </Button>
            </>
          ) : (
            <Button variant="outlined" color="error" onClick={() => setConfirmingStop(true)}>
              Stop
            </Button>
          )}
          <Button variant="outlined" onClick={() => setTakingOver(true)}>
            Take over
          </Button>
        </Paper>
      )}

      <TakeoverDialog
        open={takingOver}
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
    </>
  );
}

function TakeoverDialog({
  open,
  pending,
  onCancel,
  onConfirm,
}: {
  open: boolean;
  pending: boolean;
  onCancel: () => void;
  onConfirm: (note: string) => void;
}) {
  const [note, setNote] = useState("");
  return (
    <Dialog open={open} onClose={onCancel} aria-label="Take over this run" fullWidth maxWidth="sm">
      <DialogTitle>Take over this run</DialogTitle>
      <DialogContent>
        <DialogContentText sx={{ mb: 2 }}>
          The run stops (its branch is preserved) and you get a command to check the branch
          out locally.
        </DialogContentText>
        <TextField
          fullWidth
          multiline
          minRows={3}
          label="Tell the agent what you changed before resuming"
          placeholder="e.g. I renamed the retry helper and fixed the config loader myself."
          value={note}
          onChange={(e) => setNote(e.target.value)}
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={onCancel}>Cancel</Button>
        <Button
          variant="contained"
          color="error"
          disabled={pending}
          onClick={() => onConfirm(note)}
        >
          Stop and take over
        </Button>
      </DialogActions>
    </Dialog>
  );
}

/**
 * A monospace one-liner with a copy button — the §10.7 checkout block. A composition of
 * `Paper` + `Box component="code"` + `Button`: Material UI has no copy-to-clipboard
 * component, and the composition is three library primitives rather than a new one.
 */
export function CopyLine({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Paper
      variant="outlined"
      sx={{ display: "flex", alignItems: "center", gap: 1, px: 1, py: "4px" }}
    >
      <Box
        component="code"
        sx={{
          flex: 1,
          fontFamily: "var(--font-mono)",
          fontSize: "var(--fs-mono)",
          overflowX: "auto",
        }}
      >
        {text}
      </Box>
      <Button
        size="small"
        onClick={() => {
          void navigator.clipboard.writeText(text).then(() => {
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          });
        }}
      >
        {copied ? "Copied" : "Copy"}
      </Button>
    </Paper>
  );
}

