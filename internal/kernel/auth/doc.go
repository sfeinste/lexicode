// Package auth owns human identity: users, password hashing, sessions, invites, the auth HTTP
// endpoints and the middleware every other route hangs off (story S05, D-8).
//
// # Passwords
//
// Argon2id via golang.org/x/crypto/argon2, parameters in one place (params.go). Hashes are
// stored in PHC string format ($argon2id$v=19$m=…,t=…,p=…$salt$key), so the parameters live in
// the hash and can be tuned later without invalidating existing rows: verification always uses
// the parameters the hash was created with.
//
// # Session tokens
//
// A session token is 256 bits from crypto/rand, base64url-encoded, and lives only in the
// browser's cookie (HttpOnly, SameSite=Lax, Secure when the request arrived over TLS). The
// database stores the SHA-256 hex digest of the token as sessions.id — defense in depth: a read
// of the sessions table never yields a usable credential.
//
// # Session lifetime and sliding refresh
//
// Sessions live SessionTTL (default 30 days) from creation. On every authenticated request the
// remaining lifetime is checked; once it has fallen below half the TTL, the expiry is moved to
// now+TTL and the cookie is re-issued with a fresh Max-Age. An active user therefore never
// expires; an absent one expires 30 days after their last request. Logout deletes the row —
// revocation is server-side and immediate, whatever the cookie still says.
//
// # First-run setup
//
// With zero users in the database, every request under /api/ is answered with a 401
// application/problem+json of type "setup_required" — 401 rather than a 409-family status
// because the caller has no identity and cannot acquire one by logging in; the SPA switches on
// the type slug and routes to the setup screen. The single exception is POST /api/v1/auth/setup,
// which creates the owner. Once any user exists, setup answers 409 "already_setup", forever.
//
// # Invites
//
// POST /api/v1/invites (owner only) creates a member invite and returns a one-time URL path,
// /invite/<token>, valid for InviteTTL (default 7 days). Like session tokens, only the SHA-256
// digest is stored. POST /api/v1/auth/redeem consumes the token exactly once and creates a
// member, already logged in. No email delivery in V1 (D-8) — the owner copies the link.
//
// # CSRF
//
// The session cookie is SameSite=Lax; on top of that, CSRF (middleware.go) checks every unsafe
// method (everything but GET, HEAD, OPTIONS) under /api/. The exact policy is documented on the
// middleware: an Origin whose host differs from the request Host is rejected, and with no Origin
// present a Sec-Fetch-Site of "same-site" or "cross-site" is rejected. Requests carrying
// neither header — curl and other non-browser clients — are allowed: they cannot carry a
// browser's ambient cookie in a cross-site way.
package auth
