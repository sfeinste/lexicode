/*
 * S24 intervention surfaces on the run detail (UI spec §5.7) — converted to MUI for the
 * LEXI-13 proof of concept.
 *
 * - The steering composer — live from `queued` onward (§10.3). A sent message renders
 *   inline with the "Applied after the current step." chip and flips to delivered when the
 *   adapter accepts it (run.message SSE frame → detail refetch).
 * - Stop — a real MUI Dialog that says what stopping does, replacing the old inline
 *   "Confirm stop / Keep running" toolbar swap. Swapping a toolbar in place under the
 *   pointer is exactly the pattern a newcomer misreads.
 * - Take over — a note field ("tell the agent what you changed before resuming"), then the
 *   copy-paste checkout block, monospace with a copy button (§10.7). The block also renders
 *   statically on any run whose state_reason is `takeover`, so it survives a reload.
 *
 * Every control here is a MUI component. The composer's Enter-to-send shortcut is kept as a
 * convenience only: the Send button beside it does the same thing and is always visible, so
 * nothing on this screen is reachable by keyboard alone.
 */
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
import ListItemText from "@mui/material/ListItemText";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import TextField from "@mui/material/TextField";
import Typography from "@mui/material/Typography";
import { useState } from "react";

import type { Run, RunMessage } from "../../../lib/api/client";
import { useSteerRun, useStopRun, useTakeoverRun } from "../../../lib/api/runQueries";
import { MONO_FONT } from "../../../styles/muiTheme";

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
        // A persistent panel, not an event: it re-renders on every load of a taken-over run,
        // so it must not be an assertive live region. (MUI's `Alert` default is
        // `role="alert"` — see the header note in RunDetailPage.tsx.)
        <Alert severity="info" role="presentation">
          <Typography variant="body2" gutterBottom>
            Taken over. Check the branch out locally:
          </Typography>
          <CopyLine text={shownCheckout} />
          {run.takeover_note !== "" && (
            <Typography variant="caption" sx={{ display: "block", mt: 0.5 }}>
              Note for the next run: {run.takeover_note}
            </Typography>
          )}
        </Alert>
      )}

      {messages.length > 0 && (
        <Paper sx={{ px: 1.5, py: 0.5 }}>
          <List dense disablePadding aria-label="Steering messages">
            {messages.map((m) => (
              <ListItem key={m.id} disableGutters sx={{ gap: 1 }}>
                <ListItemText primary={m.body} slotProps={{ primary: { variant: "body2" } }} />
                <Chip
                  size="small"
                  variant="outlined"
                  color={m.state === "dropped" ? "error" : "default"}
                  label={
                    m.state === "queued"
                      ? "Applied after the current step."
                      : m.state === "delivered"
                        ? "Delivered"
                        : "Dropped"
                  }
                />
              </ListItem>
            ))}
          </List>
        </Paper>
      )}

      {!terminal && (
        <Paper component="footer" sx={{ p: 1 }}>
          <Stack direction="row" spacing={1} useFlexGap sx={{ alignItems: "center", flexWrap: "wrap" }}>
            <TextField
              size="small"
              sx={{ flexGrow: 1, minWidth: 220 }}
              label="Send a message to this run"
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
            <Button variant="outlined" color="error" onClick={() => setConfirmingStop(true)}>
              Stop run
            </Button>
            <Button variant="outlined" color="inherit" onClick={() => setTakingOver(true)}>
              Take over
            </Button>
          </Stack>
        </Paper>
      )}

      <Dialog open={confirmingStop} onClose={() => setConfirmingStop(false)}>
        <DialogTitle>Stop this run?</DialogTitle>
        <DialogContent>
          <DialogContentText>
            The agent stops where it is. Anything it has already committed stays on its
            branch, so no work is lost — but the run cannot be resumed.
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirmingStop(false)}>Keep running</Button>
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
            Stop run
          </Button>
        </DialogActions>
      </Dialog>

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
    <Dialog open={open} onClose={onCancel} fullWidth maxWidth="sm">
      <DialogTitle>Take over this run</DialogTitle>
      <DialogContent>
        <DialogContentText gutterBottom>
          The run stops (its branch is preserved) and you get a command to check the branch
          out locally.
        </DialogContentText>
        <TextField
          fullWidth
          multiline
          minRows={3}
          sx={{ mt: 1 }}
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

/** A monospace one-liner with a copy button — the §10.7 checkout block. */
export function CopyLine({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Stack direction="row" spacing={1} sx={{ alignItems: "center" }}>
      <Box
        component="code"
        sx={{
          fontFamily: MONO_FONT,
          fontSize: 12,
          px: 1,
          py: 0.5,
          border: 1,
          borderColor: "divider",
          borderRadius: 1,
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
    </Stack>
  );
}
