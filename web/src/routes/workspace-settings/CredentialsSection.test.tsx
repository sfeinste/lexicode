/*
 * S19: the credentials section's copy is part of the acceptance — the screen must tell the
 * user exactly which command to run — and the Linux-only import button must not render when
 * the server says it is unavailable.
 */
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { CredentialsStatus } from "../../lib/api/client";
import {
  CredentialsSectionView,
  SETUP_TOKEN_COMMAND,
  SETUP_TOKEN_COPY_AFTER,
  SETUP_TOKEN_COPY_BEFORE,
} from "./CredentialsSection";

function status(overrides: Partial<CredentialsStatus> = {}): CredentialsStatus {
  return {
    oauth_token: { configured: false, healthy: false, message: "no token configured" },
    env: { configured: false, healthy: false, message: "" },
    import: { available: false, path: "~/.claude/.credentials.json" },
    ...overrides,
  };
}

function subject(s: CredentialsStatus) {
  const onSave = vi.fn();
  const onImport = vi.fn();
  const onClear = vi.fn();
  const utils = render(
    <CredentialsSectionView status={s} onSave={onSave} onImport={onImport} onClear={onClear} />,
  );
  return { onSave, onImport, onClear, ...utils };
}

describe("CredentialsSection", () => {
  it("renders the exact setup copy: the command to run, verbatim", () => {
    subject(status());
    // The instruction line names the command exactly, in a code element.
    const command = screen.getByText(SETUP_TOKEN_COMMAND);
    expect(command.tagName).toBe("CODE");
    expect(command.textContent).toBe("claude setup-token");
    expect(SETUP_TOKEN_COPY_BEFORE + SETUP_TOKEN_COMMAND + SETUP_TOKEN_COPY_AFTER).toBe(
      "Run claude setup-token in a terminal, then paste its output here. Agent runs sign " +
        "in to Claude with this token; it is stored encrypted and injected into each " +
        "run's container, never shown again.",
    );
    expect(screen.getByText("Claude credentials")).toBeTruthy();
  });

  it("unconfigured: failed health dot with the server's message; no import on non-Linux", () => {
    subject(status());
    expect(screen.getByText("No token configured")).toBeTruthy();
    expect(screen.getByText("no token configured")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Import from/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "Forget token" })).toBeNull();
  });

  it("healthy: ok health dot, and a forget button", () => {
    const { onClear } = subject(
      status({ oauth_token: { configured: true, healthy: true, message: "" } }),
    );
    expect(screen.getByText("Token configured")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Forget token" }));
    expect(onClear).toHaveBeenCalledTimes(1);
  });

  it("Linux: the import button renders with the file path and fires onImport", () => {
    const { onImport } = subject(
      status({ import: { available: true, path: "~/.claude/.credentials.json" } }),
    );
    const button = screen.getByRole("button", {
      name: "Import from ~/.claude/.credentials.json",
    });
    fireEvent.click(button);
    expect(onImport).toHaveBeenCalledTimes(1);
  });

  it("saving trims the pasted token and clears the field", () => {
    const { onSave } = subject(status());
    const input = screen.getByLabelText("OAuth token") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "  sk-ant-oat01-abc  " } });
    fireEvent.click(screen.getByRole("button", { name: "Save token" }));
    expect(onSave).toHaveBeenCalledWith("sk-ant-oat01-abc");
    expect(input.value).toBe("");
  });
});
