/*
 * /setup — first-run owner creation (D-8, S05). While the database has zero users every
 * API call answers 401 setup_required and the client routes here. Once an owner exists the
 * server answers 409 already_setup and this screen points at /login.
 */
import { useMutation } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";

import { ApiProblem, authApi } from "../../lib/api/client";
import { queryClient } from "../../lib/api/queryClient";
import { AuthScreen, Field } from "./AuthScreen";
import { formValues } from "./formValues";

export function SetupPage() {
  const navigate = useNavigate();
  const setup = useMutation({
    mutationFn: authApi.setup,
    onSuccess: (user) => {
      queryClient.setQueryData(["auth", "me"], user);
      void navigate({ to: "/" });
    },
  });

  const alreadySetup =
    setup.error instanceof ApiProblem && setup.error.type === "already_setup";

  return (
    <AuthScreen
      title="Set up your workspace"
      lead="Create the owner account. Teammates join later through invite links."
      submitLabel="Create workspace"
      busy={setup.isPending}
      error={setup.error}
      onSubmit={(e) => {
        e.preventDefault();
        const v = formValues(e);
        setup.mutate({ email: v.email, display_name: v.display_name, password: v.password });
      }}
      footer={
        alreadySetup ? (
          <Link to="/login">This workspace is already set up — sign in instead</Link>
        ) : undefined
      }
    >
      <Field label="Email" name="email" type="email" autoComplete="email" error={setup.error} />
      <Field label="Display name" name="display_name" autoComplete="name" error={setup.error} />
      <Field
        label="Password"
        name="password"
        type="password"
        autoComplete="new-password"
        error={setup.error}
      />
    </AuthScreen>
  );
}
