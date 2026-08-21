import { useParams } from "@tanstack/react-router";

import { Placeholder } from "../../Placeholder";

export function WikiIndexPage() {
  const { key } = useParams({ from: "/shell/p/$key/wiki/" });
  return <Placeholder title={`${key} · Wiki`} note="The wiki tree lands in S17." />;
}
