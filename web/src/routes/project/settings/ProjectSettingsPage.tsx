import { useParams } from "@tanstack/react-router";

import { Placeholder } from "../../Placeholder";

export function ProjectSettingsPage() {
  const params = useParams({ strict: false });
  const key = params.key ?? "";
  const section = (params as { _splat?: string })._splat;
  return (
    <Placeholder
      title={section ? `${key} · Settings · ${section}` : `${key} · Settings`}
      note="Project settings sections land with their owning stories (S08 onward)."
    />
  );
}
