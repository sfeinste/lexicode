import { useParams, useSearch } from "@tanstack/react-router";

import { Placeholder } from "../../Placeholder";

export function RunDetailPage() {
  const { key, id } = useParams({ from: "/shell/p/$key/runs/$id" });
  // Selection state in the URL (interaction rule 12): ?step= and ?line= select into the
  // timeline, ?level= is the verbosity switch. Sharing a log line is copying the URL.
  const { step, line, level } = useSearch({ from: "/shell/p/$key/runs/$id" });
  return (
    <Placeholder
      title={`${key} · Run ${id}`}
      note={`Three-pane run detail lands in S22. Selection: step=${step ?? "—"} line=${line ?? "—"} level=${level ?? "normal"}.`}
    />
  );
}
