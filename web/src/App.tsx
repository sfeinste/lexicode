/**
 * The application shell.
 *
 * Story S01 ships a placeholder on purpose: the real chrome (top bar, left rail, project header,
 * router, tokens.css) is story S07, and building it before the API exists would mean building it
 * twice.
 */
export function App() {
  return (
    <main className="shell">
      <h1 className="shell__title">Lexicode</h1>
      <p className="shell__subtitle">The dashboard arrives in story S07.</p>
    </main>
  )
}
