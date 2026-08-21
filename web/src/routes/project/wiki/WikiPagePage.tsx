import { useParams } from "@tanstack/react-router";

import { Placeholder } from "../../Placeholder";

export function WikiPagePage() {
  const { key, slug } = useParams({ from: "/shell/p/$key/wiki/$slug" });
  return <Placeholder title={`${key} · Wiki · ${slug}`} note="Wiki pages land in S17." />;
}
