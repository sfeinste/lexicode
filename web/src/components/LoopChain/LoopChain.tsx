/*
 * LoopChain — the §5.9 loop chain view: the causal chain (event → run → event → …) from
 * GET /runs/{id}/chain, rendered vertically with the repeating element highlighted. Used on
 * loop-stopped run detail (S23 page) and inline in a trigger's firing history — the same
 * component, so "here is the cycle you built" reads identically everywhere.
 *
 * The "repeating element" is any event signature (kind · activity · subject) that occurs
 * more than once in the chain — the pushed-to-PR hop the loop rides on.
 *
 * D-1 (amended) — composition, not invention: `List` / `ListItem` for the chain, `Chip` for
 * the "repeats" marker, `Link` for the run hops, `Typography` for the prose. The
 * highlighting of the repeating hop is a `Paper variant="outlined"` with the `halt` hue on
 * its border, which is the §3.2 meaning of a loop stop.
 */
import Box from "@mui/material/Box";
import Chip from "@mui/material/Chip";
import List from "@mui/material/List";
import ListItem from "@mui/material/ListItem";
import Paper from "@mui/material/Paper";
import Typography from "@mui/material/Typography";

import { AppLink } from "../../theme/routerLinks";

import type { RunChainEntry } from "../../lib/api/client";
import { formatRelativeTime } from "../../lib/format/format";
import { StatusDot, type Status } from "../StatusDot/StatusDot";

export interface LoopChainProps {
  chain: RunChainEntry[];
  /** Project key, so run hops can link to their detail pages. */
  projectKey: string;
}

function eventSignature(e: { kind: string; activity_type: string; subject: string }): string {
  return `${e.kind}·${e.activity_type}·${e.subject}`;
}

/** The ▼ between hops. Decorative: the ordered list already carries the sequence. */
function Connector() {
  return (
    <Box
      component="span"
      aria-hidden="true"
      sx={{ color: "text.disabled", fontSize: "var(--fs-micro)", lineHeight: 1 }}
    >
      ▼
    </Box>
  );
}

export function LoopChain({ chain, projectKey }: LoopChainProps) {
  const signatureCounts = new Map<string, number>();
  for (const entry of chain) {
    if (entry.type === "event" && entry.event) {
      const sig = eventSignature(entry.event);
      signatureCounts.set(sig, (signatureCounts.get(sig) ?? 0) + 1);
    }
  }

  return (
    <List component="ol" aria-label="Causal chain" dense disablePadding sx={{ display: "grid" }}>
      {chain.map((entry, i) => {
        if (entry.type === "event" && entry.event) {
          const e = entry.event;
          const repeating = (signatureCounts.get(eventSignature(e)) ?? 0) > 1;
          return (
            <ListItem
              key={`e${e.id}`}
              disableGutters
              sx={{ display: "grid", justifyItems: "start", gap: "2px", py: "2px" }}
            >
              {i > 0 && <Connector />}
              <Paper
                variant="outlined"
                data-repeating={repeating || undefined}
                sx={{
                  display: "flex",
                  alignItems: "center",
                  gap: 1,
                  px: 1,
                  py: "4px",
                  width: "100%",
                  backgroundColor: "lexicode.surface2",
                  borderColor: repeating ? "lexicode.halt" : "divider",
                }}
              >
                <Box component="span" aria-hidden="true" sx={{ color: "text.disabled" }}>
                  ◆
                </Box>
                <Typography variant="body1" sx={{ flex: 1 }}>
                  {e.kind.replace(/_/g, " ")} {e.activity_type} on {e.subject}
                  {e.actor_login != null && e.actor_login !== "" && (
                    <Box component="span" sx={{ color: "text.secondary" }}>
                      {" "}
                      by @{e.actor_login}
                    </Box>
                  )}
                </Typography>
                {repeating && (
                  <Chip
                    size="small"
                    label="repeats"
                    sx={{ color: "lexicode.halt", borderColor: "lexicode.halt" }}
                    variant="outlined"
                  />
                )}
                <Typography variant="body2" sx={{ color: "text.disabled" }}>
                  {formatRelativeTime(e.occurred_at)}
                </Typography>
              </Paper>
            </ListItem>
          );
        }
        if (entry.type === "run" && entry.run) {
          const r = entry.run;
          return (
            <ListItem
              key={`r${r.id}`}
              disableGutters
              sx={{ display: "grid", justifyItems: "start", gap: "2px", py: "2px" }}
            >
              {i > 0 && <Connector />}
              <Paper
                variant="outlined"
                data-focus={r.focus || undefined}
                sx={{
                  display: "flex",
                  alignItems: "center",
                  gap: 1,
                  px: 1,
                  py: "4px",
                  width: "100%",
                  borderColor: r.focus === true ? "lexicode.borderStrong" : "divider",
                }}
              >
                <AppLink
                  to="/p/$key/runs/$id"
                  params={{ key: projectKey, id: r.id }}
                  sx={{ fontFamily: "var(--font-mono)" }}
                >
                  run #{r.seq}
                </AppLink>
                <Typography variant="body1" sx={{ flex: 1 }}>
                  {r.agent_name || r.agent_id}
                </Typography>
                <StatusDot status={r.state as Status} />
                {r.state === "loop_stopped" && r.state_reason !== "" && (
                  <Typography variant="body2" sx={{ color: "lexicode.halt" }}>
                    {r.state_reason}
                  </Typography>
                )}
              </Paper>
            </ListItem>
          );
        }
        return null;
      })}
    </List>
  );
}
