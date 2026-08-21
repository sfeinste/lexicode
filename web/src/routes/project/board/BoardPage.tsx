import { useParams, useSearch } from "@tanstack/react-router";

import { Placeholder } from "../../Placeholder";

export function BoardPage() {
  const { key } = useParams({ from: "/shell/p/$key/board" });
  // Selection state lives in the URL (interaction rule 12): ?view=backlog is the backlog,
  // ?group_by= the grouping. Validated in the route definition.
  const { view } = useSearch({ from: "/shell/p/$key/board" });
  return (
    <Placeholder
      title={view === "backlog" ? `${key} · Backlog` : `${key} · Board`}
      note="Columns, cards and the needs-you lane land in S13–S15."
    />
  );
}
