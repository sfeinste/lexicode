/*
 * /invite/:token — invite redemption (D-8: copyable one-time links, no email delivery).
 * Redeeming creates a member account and signs it in.
 */
import { useMutation } from "@tanstack/react-query";
import { useNavigate, useParams } from "@tanstack/react-router";

import { ApiProblem, authApi } from "../../lib/api/client";
import { queryClient } from "../../lib/api/queryClient";
import { AuthScreen, Field } from "./AuthScreen";
import { formValues } from "./formValues";

export function InvitePage() {
  const { token } = useParams({ from: "/invite/$token" });
  const navigate = useNavigate();
  const redeem = useMutation({
    mutationFn: (body: { email: string; display_name: string; password: string }) =>
      authApi.redeemInvite(token, body),
    onSuccess: (user) => {
      queryClient.setQueryData(["auth", "me"], user);
      void navigate({ to: "/" });
    },
  });

  const dead =
    redeem.error instanceof ApiProblem &&
    (redeem.error.type === "invite_invalid" || redeem.error.type === "invite_expired");

  return (
    <AuthScreen
      title="Join this workspace"
      lead="You were invited. Create your account to join."
      submitLabel="Join"
      busy={redeem.isPending}
      error={redeem.error}
      onSubmit={(e) => {
        e.preventDefault();
        const v = formValues(e);
        redeem.mutate({ email: v.email, display_name: v.display_name, password: v.password });
      }}
      footer={dead ? <span>Ask the workspace owner for a fresh invite link.</span> : undefined}
    >
      <Field label="Email" name="email" type="email" autoComplete="email" error={redeem.error} />
      <Field label="Display name" name="display_name" autoComplete="name" error={redeem.error} />
      <Field
        label="Password"
        name="password"
        type="password"
        autoComplete="new-password"
        error={redeem.error}
      />
    </AuthScreen>
  );
}
