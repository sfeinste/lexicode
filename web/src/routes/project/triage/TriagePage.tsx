import { useParams } from "@tanstack/react-router";

import { EmptyState } from "../../../components/EmptyState/EmptyState";
import { Placeholder } from "../../Placeholder";

export function TriagePage() {
  const { key } = useParams({ from: "/shell/p/$key/triage" });
  return (
    <div>
      <Placeholder title={`${key} · Triage`} note="The intake queue lands in S16." />
      <EmptyState
        headline="Nothing to triage"
        body="Tickets created by triggers and agents land here first."
      />
    </div>
  );
}
