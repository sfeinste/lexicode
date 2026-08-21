/*
 * The create-project flow (S08), reached from the Home empty state's single CTA and the
 * Projects section header. Field-level validation problems (400 validation_failed) render
 * inline next to the input that caused them — a duplicate key names `key`.
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";

import { ApiProblem, projectsApi, type CreateProjectRequest } from "../../lib/api/client";
import { projectKeys } from "../../lib/api/projectQueries";
import styles from "./home.module.css";

const PALETTE = ["#7c5cff", "#00a884", "#ff8a3d", "#5b8def", "#d16ba5", "#e5484d"];

/** Suggest an uppercase key from the name: "Payments service" → "PAYM". */
function suggestKey(name: string): string {
  return name
    .toUpperCase()
    .replace(/[^A-Z0-9]/g, "")
    .replace(/^[0-9]+/, "")
    .slice(0, 4);
}

export function CreateProjectDialog({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState("");
  const [key, setKey] = useState("");
  const [keyTouched, setKeyTouched] = useState(false);
  const [color, setColor] = useState(PALETTE[0]);
  const [description, setDescription] = useState("");
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const nameRef = useRef<HTMLInputElement>(null);
  const navigate = useNavigate();
  const qc = useQueryClient();

  useEffect(() => nameRef.current?.focus(), []);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const create = useMutation({
    mutationFn: (body: CreateProjectRequest) => projectsApi.create(body),
    onSuccess: (project) => {
      void qc.invalidateQueries({ queryKey: projectKeys.all });
      onClose();
      void navigate({ to: "/p/$key", params: { key: project.key } });
    },
    onError: (err) => {
      if (err instanceof ApiProblem && err.errors?.length) {
        const map: Record<string, string> = {};
        for (const fe of err.errors) map[fe.field] = fe.message;
        setFieldErrors(map);
        setFormError(null);
      } else {
        setFieldErrors({});
        setFormError(err instanceof Error ? err.message : "Something went wrong.");
      }
    },
  });

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    setFieldErrors({});
    setFormError(null);
    create.mutate({ key, name, color, description: description || undefined });
  };

  return (
    <div className={styles.overlay} role="presentation" onClick={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Create project"
        className={styles.dialog}
        onClick={(e) => e.stopPropagation()}
      >
        <h2 className={styles.dialogTitle}>Create project</h2>
        <form onSubmit={submit} className={styles.form}>
          <label className={styles.field}>
            Name
            <input
              ref={nameRef}
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                if (!keyTouched) setKey(suggestKey(e.target.value));
              }}
              placeholder="Payments"
            />
            {fieldErrors.name && (
              <span role="alert" className={styles.fieldError}>
                {fieldErrors.name}
              </span>
            )}
          </label>
          <label className={styles.field}>
            Key
            <input
              value={key}
              onChange={(e) => {
                setKeyTouched(true);
                setKey(e.target.value.toUpperCase());
              }}
              placeholder="PAY"
              className={styles.keyInput}
            />
            <span className={styles.fieldHint}>
              Uppercase, 2–10 characters. Drives ticket keys like {key || "PAY"}-14.
            </span>
            {fieldErrors.key && (
              <span role="alert" className={styles.fieldError}>
                {fieldErrors.key}
              </span>
            )}
          </label>
          <div className={styles.field}>
            Color
            <div className={styles.swatches} role="radiogroup" aria-label="Project color">
              {PALETTE.map((c) => (
                <button
                  key={c}
                  type="button"
                  role="radio"
                  aria-checked={c === color}
                  aria-label={`Color ${c}`}
                  className={styles.swatch}
                  style={{ background: c }}
                  data-selected={c === color || undefined}
                  onClick={() => setColor(c)}
                />
              ))}
            </div>
            {fieldErrors.color && (
              <span role="alert" className={styles.fieldError}>
                {fieldErrors.color}
              </span>
            )}
          </div>
          <label className={styles.field}>
            Description
            <textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={2}
              placeholder="What this project is (optional)"
            />
          </label>
          {formError && (
            <p role="alert" className={styles.fieldError}>
              {formError}
            </p>
          )}
          <div className={styles.dialogActions}>
            <button type="button" className={styles.secondaryButton} onClick={onClose}>
              Cancel
            </button>
            <button type="submit" className={styles.cta} disabled={create.isPending}>
              {create.isPending ? "Creating…" : "Create project"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
