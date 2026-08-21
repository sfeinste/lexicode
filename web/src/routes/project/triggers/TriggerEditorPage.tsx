import { useParams } from "@tanstack/react-router";

import { Placeholder } from "../../Placeholder";

export function TriggerEditorPage() {
  const { key, id } = useParams({ from: "/shell/p/$key/triggers/$id" });
  return (
    <Placeholder title={`${key} · Trigger ${id}`} note="The WHEN/IF/THEN editor lands in S24." />
  );
}
