/*
 * InlineElicitation (S36): the S24 respond surface — QuestionRow / ApprovalRow, exactly as
 * they render on the run detail — embedded against a run id, so a question is answered and
 * an approval decided FROM the home needs-you card or an inbox row, without a navigation.
 * That is the whole point of the strip (UI spec §5.1): the run resumes while the URL never
 * changes.
 *
 * It fetches the run detail (the elicitations and the agent id) and the transcript (the
 * elicitation's level-0 activity row, whose payload the question form renders from), finds
 * the pending elicitation, and hands both to the same ElicitationDetail the timeline uses —
 * one code path for answering, wherever the answer happens.
 *
 * D-1 (amended): `Box` + `Typography`. The four states below are prose, not controls, so
 * this file is the smallest possible slice of the conversion — but it has to convert with
 * the renderers, because it embeds them.
 */
import Box from "@mui/material/Box";
import Typography from "@mui/material/Typography";

import { useRunActivitiesQuery, useRunDetailQuery } from "../../../lib/api/runQueries";
import { ElicitationDetail } from "./renderers";

/** The three not-yet / can't states: quiet, secondary, one line. */
function Quiet({ children, alert }: { children: string; alert?: boolean }) {
  return (
    <Typography
      variant="body2"
      role={alert === true ? "alert" : undefined}
      sx={{ color: "text.secondary", py: 1 }}
    >
      {children}
    </Typography>
  );
}

export function InlineElicitation({
  runId,
  projectKey,
}: {
  runId: string;
  projectKey: string;
}) {
  const detail = useRunDetailQuery(runId);
  const activities = useRunActivitiesQuery(runId);

  if (detail.isPending || activities.isPending) {
    return <Quiet>Loading the question…</Quiet>;
  }
  if (detail.isError || activities.isError) {
    return <Quiet alert>The run could not load — open it to respond.</Quiet>;
  }

  const pending = detail.data.elicitations.find((el) => el.state === "pending");
  if (pending === undefined) {
    // Answered elsewhere (another tab, the run detail) between render and now.
    return <Quiet>Nothing is pending — already handled.</Quiet>;
  }
  const activity = activities.data.activities.find((a) => a.seq === pending.activity_seq);
  if (activity === undefined) {
    return <Quiet alert>The question could not load — open the run to respond.</Quiet>;
  }
  return (
    <Box sx={{ mt: 1 }}>
      <ElicitationDetail
        a={activity}
        intervene={{
          projectKey,
          agentID: detail.data.run.agent_id,
          elicitations: detail.data.elicitations,
        }}
      />
    </Box>
  );
}
