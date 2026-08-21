import { useParams } from "@tanstack/react-router";

import { Placeholder } from "../../Placeholder";

export function AgentDetailPage() {
  const { key, id } = useParams({ from: "/shell/p/$key/agents/$id" });
  return <Placeholder title={`${key} · Agent ${id}`} note="Agent config lands in S18." />;
}
