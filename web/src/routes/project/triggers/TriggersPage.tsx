import { useParams } from "@tanstack/react-router";

import { EmptyState } from "../../../components/EmptyState/EmptyState";
import { Placeholder } from "../../Placeholder";

export function TriggersPage() {
  const { key } = useParams({ from: "/shell/p/$key/triggers/" });
  return (
    <div>
      <Placeholder title={`${key} · Triggers`} note="Trigger cards land in S24." />
      <EmptyState
        headline="No triggers yet"
        body="Start an agent automatically when something happens in the repo."
      />
    </div>
  );
}
