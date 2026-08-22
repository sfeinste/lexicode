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
 */
import { useRunActivitiesQuery, useRunDetailQuery } from "../../../lib/api/runQueries";
import { ElicitationDetail } from "./renderers";
import styles from "./runs.module.css";

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
    return <p className={styles.inlineElicitationQuiet}>Loading the question…</p>;
  }
  if (detail.isError || activities.isError) {
    return (
      <p role="alert" className={styles.inlineElicitationQuiet}>
        The run could not load — open it to respond.
      </p>
    );
  }

  const pending = detail.data.elicitations.find((el) => el.state === "pending");
  if (pending === undefined) {
    // Answered elsewhere (another tab, the run detail) between render and now.
    return <p className={styles.inlineElicitationQuiet}>Nothing is pending — already handled.</p>;
  }
  const activity = activities.data.activities.find((a) => a.seq === pending.activity_seq);
  if (activity === undefined) {
    return (
      <p role="alert" className={styles.inlineElicitationQuiet}>
        The question could not load — open the run to respond.
      </p>
    );
  }
  return (
    <div className={styles.inlineElicitation}>
      <ElicitationDetail
        a={activity}
        intervene={{
          projectKey,
          agentID: detail.data.run.agent_id,
          elicitations: detail.data.elicitations,
        }}
      />
    </div>
  );
}
