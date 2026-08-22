/*
 * The trigger list (UI spec §5.9): each rule a prose card — WHEN/IF/THEN lines, the outcome
 * sparkline colored by class, the per-outcome breakdown in words, the actor-suppression
 * line, last-fired and the enable toggle. Disabled rules render muted. Card click opens the
 * editor; the toggle mutates in place.
 */
import { Link, useNavigate, useParams } from "@tanstack/react-router";

import { EmptyState } from "../../../components/EmptyState/EmptyState";
import { useAgentsQuery } from "../../../lib/api/agentQueries";
import {
  useTriggerCatalogQuery,
  useTriggersQuery,
  useUpdateTrigger,
} from "../../../lib/api/triggerQueries";
import { TriggerCard } from "./TriggerCard";
import styles from "./triggers.module.css";

export function TriggersPage() {
  const { key } = useParams({ from: "/shell/p/$key/triggers/" });
  const navigate = useNavigate();
  const triggers = useTriggersQuery(key);
  const catalog = useTriggerCatalogQuery(key);
  const agents = useAgentsQuery(key);
  const update = useUpdateTrigger(key);

  if (triggers.isPending || catalog.isPending) {
    return <p className={styles.muted}>Loading triggers…</p>;
  }
  if (triggers.isError || catalog.isError || catalog.data === undefined) {
    return <p className={styles.errorText}>Triggers failed to load.</p>;
  }

  const list = triggers.data?.triggers ?? [];
  const agentNames = new Map((agents.data?.agents ?? []).map((a) => [a.id, a.name]));

  return (
    <div className={styles.page}>
      <div className={styles.pageTitle}>
        <h1>Triggers</h1>
        <div className={styles.titleActions}>
          <Link to="/p/$key/triggers/$id" params={{ key, id: "new" }} className={styles.newButton}>
            New trigger
          </Link>
        </div>
      </div>
      {list.length === 0 ? (
        <EmptyState
          headline="No triggers yet"
          body="Start an agent automatically when something happens in the repo. Rules read as prose: WHEN an event arrives, IF it matches, THEN act."
          primary={
            <Link to="/p/$key/triggers/$id" params={{ key, id: "new" }} className={styles.newButton}>
              Create a trigger
            </Link>
          }
        />
      ) : (
        <div className={styles.cardList}>
          {list.map((tr) => (
            <TriggerCard
              key={tr.id}
              trigger={tr}
              catalog={catalog.data}
              agentNames={agentNames}
              onToggle={(enabled) => update.mutate({ id: tr.id, body: { enabled } })}
              wrap={(children) => (
                <button
                  type="button"
                  className={styles.cardBody}
                  onClick={() =>
                    void navigate({
                      to: "/p/$key/triggers/$id",
                      params: { key, id: tr.id },
                    })
                  }
                >
                  {children}
                </button>
              )}
            />
          ))}
        </div>
      )}
    </div>
  );
}
