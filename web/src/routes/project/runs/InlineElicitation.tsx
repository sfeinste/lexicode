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
 * LEXI-13 note: those renderers are now MUI, and home/inbox are not converted yet, so this
 * component mounts its own MuiThemeProvider. That is the general shape of the problem a
 * staged migration has to handle — a shared component pulls the new library into screens
 * that have not moved. Nesting MUI's ThemeProvider is supported and cheap, and it keeps the
 * blast radius exactly at this strip. It comes out when home and inbox convert
 * (plan/06-ui-redesign-plan.md, stage 4).
 */
import Typography from "@mui/material/Typography";

import { useRunActivitiesQuery, useRunDetailQuery } from "../../../lib/api/runQueries";
import { MuiThemeProvider } from "../../../styles/MuiThemeProvider";
import { ElicitationDetail } from "./renderers";

export function InlineElicitation({
  runId,
  projectKey,
}: {
  runId: string;
  projectKey: string;
}) {
  return (
    <MuiThemeProvider>
      <InlineElicitationBody runId={runId} projectKey={projectKey} />
    </MuiThemeProvider>
  );
}

function InlineElicitationBody({
  runId,
  projectKey,
}: {
  runId: string;
  projectKey: string;
}) {
  const detail = useRunDetailQuery(runId);
  const activities = useRunActivitiesQuery(runId);

  if (detail.isPending || activities.isPending) {
    return (
      <Typography variant="body2" color="text.secondary">
        Loading the question…
      </Typography>
    );
  }
  if (detail.isError || activities.isError) {
    return (
      <Typography variant="body2" color="error" role="alert">
        The run could not load — open it to respond.
      </Typography>
    );
  }

  const pending = detail.data.elicitations.find((el) => el.state === "pending");
  if (pending === undefined) {
    // Answered elsewhere (another tab, the run detail) between render and now.
    return (
      <Typography variant="body2" color="text.secondary">
        Nothing is pending — already handled.
      </Typography>
    );
  }
  const activity = activities.data.activities.find((a) => a.seq === pending.activity_seq);
  if (activity === undefined) {
    return (
      <Typography variant="body2" color="error" role="alert">
        The question could not load — open the run to respond.
      </Typography>
    );
  }
  return (
    <ElicitationDetail
      a={activity}
      intervene={{
        projectKey,
        agentID: detail.data.run.agent_id,
        elicitations: detail.data.elicitations,
      }}
    />
  );
}
