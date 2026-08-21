import type { FormEvent } from "react";

/** Read a submitted form's named inputs into a plain record. */
export function formValues(e: FormEvent<HTMLFormElement>): Record<string, string> {
  const data = new FormData(e.currentTarget);
  const out: Record<string, string> = {};
  for (const [k, v] of data.entries()) out[k] = String(v);
  return out;
}
