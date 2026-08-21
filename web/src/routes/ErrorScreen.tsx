/*
 * The route-level error boundary body. TanStack Router catches a render/loader error and
 * renders this in place of the route, keeping the chrome alive.
 */
import { EmptyState } from "../components/EmptyState/EmptyState";

export function ErrorScreen({ error }: { error: Error }) {
  return (
    <EmptyState
      headline="Something broke on this screen"
      body={error.message || "An unexpected error occurred rendering this route."}
      primary={<button onClick={() => window.location.reload()}>Reload</button>}
    />
  );
}
