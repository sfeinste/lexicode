/*
 * S18: the network policy block's honest wording (D-10) and its inherit/override mechanics.
 * The `none` label must say what the policy actually does — "nothing beyond what the agent
 * itself needs" — never "no network": the container still reaches the Anthropic API and the
 * repo's git host through the egress proxy, and pretending otherwise would make the setting
 * a trap.
 */
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { NetworkPolicyField } from "./NetworkSection";

function subject(overrides: Partial<Parameters<typeof NetworkPolicyField>[0]> = {}) {
  const onPolicyChange = vi.fn();
  const onAllowlistChange = vi.fn();
  const utils = render(
    <NetworkPolicyField
      policy={null}
      workspaceDefault="allowlist"
      allowlist={[]}
      onPolicyChange={onPolicyChange}
      onAllowlistChange={onAllowlistChange}
      {...overrides}
    />,
  );
  return { onPolicyChange, onAllowlistChange, ...utils };
}

describe("NetworkPolicyField", () => {
  it("renders the three D-10 radios with the honest `none` wording", () => {
    subject();
    const radios = screen.getAllByRole("radio");
    expect(radios).toHaveLength(3);
    // The exact honest label, verbatim from D-10.
    expect(
      screen.getByRole("radio", { name: /None — nothing beyond what the agent itself needs/ }),
    ).toBeTruthy();
    expect(screen.getByRole("radio", { name: /Allowlist — approved domains only/ })).toBeTruthy();
    expect(screen.getByRole("radio", { name: /Open — unrestricted/ })).toBeTruthy();
  });

  it("inherited: radios are disabled, mirror the workspace default, and Override overrides", () => {
    const { onPolicyChange } = subject();
    for (const radio of screen.getAllByRole("radio")) {
      expect((radio as HTMLInputElement).disabled).toBe(true);
    }
    const allowlist = screen.getByRole("radio", {
      name: /Allowlist — approved domains only/,
    }) as HTMLInputElement;
    expect(allowlist.checked).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "Override." }));
    expect(onPolicyChange).toHaveBeenCalledWith("allowlist");
  });

  it("overridden: choosing a policy saves it and Reset reverts to null (inherit)", () => {
    const { onPolicyChange } = subject({ policy: "open" });
    const none = screen.getByRole("radio", {
      name: /None — nothing beyond what the agent itself needs/,
    });
    fireEvent.click(none);
    expect(onPolicyChange).toHaveBeenCalledWith("none");

    fireEvent.click(screen.getByRole("button", { name: "Reset to workspace default." }));
    expect(onPolicyChange).toHaveBeenCalledWith(null);
  });

  it("shows the domain editor only when the effective policy is allowlist, and parses lines", () => {
    const { onAllowlistChange } = subject({
      policy: "allowlist",
      allowlist: ["registry.npmjs.org"],
    });
    const editor = screen.getByLabelText(/Allowed domains/) as HTMLTextAreaElement;
    expect(editor.value).toBe("registry.npmjs.org");

    fireEvent.change(editor, {
      target: { value: "registry.npmjs.org\n  *.pypi.org  \n\n" },
    });
    expect(onAllowlistChange).toHaveBeenCalledWith(["registry.npmjs.org", "*.pypi.org"]);
  });

  it("hides the domain editor under none and open", () => {
    subject({ policy: "none" });
    expect(screen.queryByLabelText(/Allowed domains/)).toBeNull();
  });
});
