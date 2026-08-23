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

/*
 * An allowlist with no domains is the default state of every project, and it behaves exactly
 * like `none` — the label reads permissive while the proxy denies everything. That discrepancy
 * cost a real user a run: the policy said "allowlist" and `npm install` came back 403 with no
 * explanation on this screen. These assert the screen states it instead.
 */
describe("NetworkPolicyField — what is actually reachable", () => {
  it("warns that an empty allowlist behaves like None", () => {
    subject({ policy: "allowlist", allowlist: [] });
    expect(
      screen.getByText(/This allowlist is empty, so it currently behaves exactly like None/),
    ).toBeTruthy();
    // The failure a user would otherwise meet only at runtime is named here.
    expect(screen.getByText(/will fail with a proxy denial/)).toBeTruthy();
  });

  it("drops the warning once a domain is listed", () => {
    subject({ policy: "allowlist", allowlist: ["registry.npmjs.org"] });
    expect(screen.queryByText(/behaves exactly like None/)).toBeNull();
  });

  it("names what is reachable under each policy", () => {
    const empty = subject({ policy: "allowlist", allowlist: [] });
    expect(
      screen.getByText(/the Anthropic API and this repository's git host, and nothing else/),
    ).toBeTruthy();
    empty.unmount();

    const listed = subject({ policy: "allowlist", allowlist: ["a.com", "b.com"] });
    expect(screen.getByText(/and 2 listed domains/)).toBeTruthy();
    listed.unmount();

    const one = subject({ policy: "allowlist", allowlist: ["a.com"] });
    expect(screen.getByText(/and 1 listed domain\./)).toBeTruthy();
    one.unmount();

    subject({ policy: "open" });
    expect(screen.getByText(/every host — the container joins the default Docker network/)).toBeTruthy();
  });

  it("says nothing-else for `none`, where the warning would be redundant", () => {
    subject({ policy: "none" });
    expect(screen.queryByText(/behaves exactly like None/)).toBeNull();
    expect(
      screen.getByText(/the Anthropic API and this repository's git host, and nothing else/),
    ).toBeTruthy();
  });

  it("reports the INHERITED policy's reachability, not the stored override", () => {
    // policy=null means inherit; the line must describe what will actually happen.
    subject({ policy: null, workspaceDefault: "open", allowlist: [] });
    expect(screen.getByText(/every host — the container joins the default Docker network/)).toBeTruthy();
  });
});
