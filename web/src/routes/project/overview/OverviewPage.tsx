import { useParams } from "@tanstack/react-router";

import { Placeholder } from "../../Placeholder";

export function OverviewPage() {
  const { key } = useParams({ from: "/shell/p/$key/" });
  return <Placeholder title={`${key} · Overview`} note="The About card lands in S08." />;
}
