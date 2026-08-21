import { useParams } from "@tanstack/react-router";

import { EmptyState } from "../../../components/EmptyState/EmptyState";
import { Placeholder } from "../../Placeholder";

export function AgentsPage() {
  const { key } = useParams({ from: "/shell/p/$key/agents/" });
  return (
    <div>
      <Placeholder title={`${key} · Agents`} note="The roster lands in S18." />
      <EmptyState
        headline="No agents yet"
        body="An agent is a name, a prompt, and a set of permissions."
      />
    </div>
  );
}
