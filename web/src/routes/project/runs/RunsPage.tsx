import { useParams } from "@tanstack/react-router";

import { EmptyState } from "../../../components/EmptyState/EmptyState";
import { Placeholder } from "../../Placeholder";

export function RunsPage() {
  const { key } = useParams({ from: "/shell/p/$key/runs/" });
  return (
    <div>
      <Placeholder title={`${key} · Runs`} note="The run list lands in S21." />
      <EmptyState
        headline="No runs yet"
        body="Delegate a ticket to an agent and its run appears here."
      />
    </div>
  );
}
