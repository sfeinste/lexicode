/*
 * /login — password sign-in against POST /api/v1/auth/login. A 401 invalid_credentials is a
 * form error here, never a redirect (the client's skipAuthRedirect).
 */
import { useMutation } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";

import { authApi } from "../../lib/api/client";
import { queryClient } from "../../lib/api/queryClient";
import { AuthScreen, Field } from "./AuthScreen";
import { formValues } from "./formValues";

export function LoginPage() {
  const navigate = useNavigate();
  const login = useMutation({
    mutationFn: authApi.login,
    onSuccess: (user) => {
      queryClient.setQueryData(["auth", "me"], user);
      void navigate({ to: "/" });
    },
  });

  return (
    <AuthScreen
      title="Sign in"
      submitLabel="Sign in"
      busy={login.isPending}
      error={login.error}
      onSubmit={(e) => {
        e.preventDefault();
        const v = formValues(e);
        login.mutate({ email: v.email, password: v.password });
      }}
    >
      <Field label="Email" name="email" type="email" autoComplete="email" error={login.error} />
      <Field
        label="Password"
        name="password"
        type="password"
        autoComplete="current-password"
        error={login.error}
      />
    </AuthScreen>
  );
}
