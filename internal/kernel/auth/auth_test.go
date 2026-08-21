package auth_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// clock is the injectable time source: tests move it to expire sessions and invites without
// sleeping.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// env is one auth stack over a real temp-file store, served exactly as production serves it:
// the auth routes plus probe routes on a mux, wrapped in CSRF and the setup gate.
type env struct {
	t      *testing.T
	dbPath string
	st     *store.Store
	svc    *auth.Service
	srv    *httptest.Server
	clock  *clock
}

func newEnv(t *testing.T, mod func(*auth.Options)) *env {
	t.Helper()
	e := &env{
		t:      t,
		dbPath: filepath.Join(t.TempDir(), "auth.db"),
		clock:  &clock{t: time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)},
	}
	e.openStore()

	opts := auth.Options{Store: e.st, Now: e.clock.Now}
	if mod != nil {
		mod(&opts)
	}
	e.svc = auth.New(opts)

	mux := http.NewServeMux()
	e.svc.Routes(mux)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	mux.Handle("GET /api/v1/probe/authed", e.svc.RequireAuth(ok))
	mux.Handle("GET /api/v1/projects/{key}/probe",
		e.svc.RequireAuth(e.svc.RequireProjectMember(ok)))

	e.srv = httptest.NewServer(auth.CSRF(e.svc.SetupGate(mux)))
	t.Cleanup(e.srv.Close)
	t.Cleanup(func() { _ = e.st.Close() })
	return e
}

func (e *env) openStore() {
	e.t.Helper()
	st, err := store.Open(store.Options{Path: e.dbPath})
	if err != nil {
		e.t.Fatalf("open store: %v", err)
	}
	if _, err := st.Migrate(e.t.Context()); err != nil {
		e.t.Fatalf("migrate: %v", err)
	}
	e.st = st
}

// do sends one request and returns the status, the body, and any session cookie the response
// set. cookie and headers may be nil.
func (e *env) do(method, path, body string, cookie *http.Cookie, headers map[string]string) (int, string, *http.Cookie) {
	e.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(e.t.Context(), method, e.srv.URL+path, reader)
	if err != nil {
		e.t.Fatalf("build request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("read body: %v", err)
	}
	var sess *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookie {
			sess = c
		}
	}
	return resp.StatusCode, string(raw), sess
}

// setupOwner runs first-run setup and returns the owner's session cookie.
func (e *env) setupOwner() *http.Cookie {
	e.t.Helper()
	status, body, cookie := e.do(http.MethodPost, "/api/v1/auth/setup",
		`{"email":"ada@example.com","display_name":"Ada","password":"correct horse"}`, nil, nil)
	if status != http.StatusCreated {
		e.t.Fatalf("setup status = %d, body = %s, want 201", status, body)
	}
	if cookie == nil {
		e.t.Fatal("setup did not set a session cookie")
	}
	return cookie
}

// invite creates a member invite as the owner and returns the raw token from the one-time path.
func (e *env) invite(owner *http.Cookie) string {
	e.t.Helper()
	status, body, _ := e.do(http.MethodPost, "/api/v1/invites", "{}", owner, nil)
	if status != http.StatusCreated {
		e.t.Fatalf("create invite status = %d, body = %s, want 201", status, body)
	}
	var inv struct {
		Path      string `json:"path"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(body), &inv); err != nil {
		e.t.Fatalf("decode invite %s: %v", body, err)
	}
	if !strings.HasPrefix(inv.Path, "/invite/") {
		e.t.Fatalf("invite path = %q, want /invite/<token>", inv.Path)
	}
	return strings.TrimPrefix(inv.Path, "/invite/")
}

// redeem consumes an invite token as a new member and returns their session cookie.
func (e *env) redeem(token, email string) *http.Cookie {
	e.t.Helper()
	status, body, cookie := e.do(http.MethodPost, "/api/v1/auth/redeem",
		`{"token":"`+token+`","email":"`+email+`","display_name":"Member","password":"long enough"}`,
		nil, nil)
	if status != http.StatusCreated {
		e.t.Fatalf("redeem status = %d, body = %s, want 201", status, body)
	}
	if cookie == nil {
		e.t.Fatal("redeem did not set a session cookie")
	}
	return cookie
}

func wantProblem(t *testing.T, status int, body string, wantStatus int, wantType string) {
	t.Helper()
	if status != wantStatus {
		t.Errorf("status = %d, body = %s, want %d", status, body, wantStatus)
	}
	if !strings.Contains(body, `"type":"`+wantType+`"`) {
		t.Errorf("body = %s, want problem type %q", body, wantType)
	}
}

// ---------------------------------------------------------------- first run and setup -----

func TestSetupGateBlocksEverythingButSetup(t *testing.T) {
	e := newEnv(t, nil)

	for _, probe := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/auth/me", ""},
		{http.MethodPost, "/api/v1/auth/login", `{"email":"a@b.c","password":"whatever!"}`},
		{http.MethodGet, "/api/v1/probe/authed", ""},
		{http.MethodPost, "/api/v1/invites", "{}"},
	} {
		status, body, _ := e.do(probe.method, probe.path, probe.body, nil, nil)
		if status != http.StatusUnauthorized || !strings.Contains(body, `"setup_required"`) {
			t.Errorf("%s %s pre-setup = %d %s, want 401 setup_required", probe.method, probe.path, status, body)
		}
	}

	cookie := e.setupOwner()
	status, body, _ := e.do(http.MethodGet, "/api/v1/auth/me", "", cookie, nil)
	if status != http.StatusOK || !strings.Contains(body, `"role":"owner"`) {
		t.Errorf("me after setup = %d %s, want 200 with role owner", status, body)
	}
}

func TestSetupIsRefusedOnceAUserExists(t *testing.T) {
	e := newEnv(t, nil)
	e.setupOwner()

	status, body, _ := e.do(http.MethodPost, "/api/v1/auth/setup",
		`{"email":"eve@example.com","display_name":"Eve","password":"password1"}`, nil, nil)
	wantProblem(t, status, body, http.StatusConflict, "already_setup")
}

// ------------------------------------------------------------------- login and logout -----

func TestLoginRightAndWrongPassword(t *testing.T) {
	e := newEnv(t, nil)
	e.setupOwner()

	status, body, _ := e.do(http.MethodPost, "/api/v1/auth/login",
		`{"email":"ada@example.com","password":"wrong horse"}`, nil, nil)
	wantProblem(t, status, body, http.StatusUnauthorized, "invalid_credentials")

	// An unknown email answers identically to a wrong password.
	status, body, _ = e.do(http.MethodPost, "/api/v1/auth/login",
		`{"email":"nobody@example.com","password":"wrong horse"}`, nil, nil)
	wantProblem(t, status, body, http.StatusUnauthorized, "invalid_credentials")

	status, body, cookie := e.do(http.MethodPost, "/api/v1/auth/login",
		`{"email":"ada@example.com","password":"correct horse"}`, nil, nil)
	if status != http.StatusOK || cookie == nil {
		t.Fatalf("login = %d %s (cookie %v), want 200 with a session cookie", status, body, cookie)
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie = %+v, want HttpOnly SameSite=Lax", cookie)
	}

	status, body, _ = e.do(http.MethodGet, "/api/v1/auth/me", "", cookie, nil)
	if status != http.StatusOK || !strings.Contains(body, `"ada@example.com"`) {
		t.Errorf("me = %d %s, want 200 for ada", status, body)
	}
}

func TestLogoutRevokesTheSessionServerSide(t *testing.T) {
	e := newEnv(t, nil)
	cookie := e.setupOwner()

	status, body, _ := e.do(http.MethodPost, "/api/v1/auth/logout", "", cookie, nil)
	if status != http.StatusNoContent {
		t.Fatalf("logout = %d %s, want 204", status, body)
	}

	// The browser still holds the token; the row is gone, so it is worthless.
	status, body, _ = e.do(http.MethodGet, "/api/v1/auth/me", "", cookie, nil)
	wantProblem(t, status, body, http.StatusUnauthorized, "unauthenticated")

	// Logging out again is a quiet no-op.
	status, _, _ = e.do(http.MethodPost, "/api/v1/auth/logout", "", cookie, nil)
	if status != http.StatusNoContent {
		t.Errorf("second logout = %d, want 204", status)
	}
}

// ------------------------------------------------------------------------- sessions -----

func TestExpiredSessionIs401SessionExpired(t *testing.T) {
	e := newEnv(t, func(o *auth.Options) { o.SessionTTL = time.Hour })
	cookie := e.setupOwner()

	e.clock.Advance(time.Hour + time.Minute)
	status, body, _ := e.do(http.MethodGet, "/api/v1/auth/me", "", cookie, nil)
	wantProblem(t, status, body, http.StatusUnauthorized, "session_expired")
}

func TestSlidingRefreshExtendsExpiry(t *testing.T) {
	e := newEnv(t, func(o *auth.Options) { o.SessionTTL = 10 * time.Hour })
	cookie := e.setupOwner()
	sessionID := auth.HashToken(cookie.Value)

	expiresAt := func() string {
		sess, err := e.st.Sessions().ByID(t.Context(), sessionID)
		if err != nil {
			t.Fatalf("read session: %v", err)
		}
		return sess.ExpiresAt
	}
	initial := expiresAt()

	// More than half the TTL remains: no refresh, no re-issued cookie.
	e.clock.Advance(2 * time.Hour)
	status, _, reissued := e.do(http.MethodGet, "/api/v1/auth/me", "", cookie, nil)
	if status != http.StatusOK {
		t.Fatalf("me at +2h = %d, want 200", status)
	}
	if reissued != nil {
		t.Errorf("cookie re-issued at +2h with %s of 10h left; refresh should start past halfway", "8h")
	}
	if got := expiresAt(); got != initial {
		t.Errorf("expiry moved at +2h: %s → %s, want unchanged", initial, got)
	}

	// Past halfway: the expiry slides to now+TTL and the cookie is re-issued to match.
	e.clock.Advance(4 * time.Hour)
	status, _, reissued = e.do(http.MethodGet, "/api/v1/auth/me", "", cookie, nil)
	if status != http.StatusOK {
		t.Fatalf("me at +6h = %d, want 200", status)
	}
	if reissued == nil {
		t.Fatal("no re-issued cookie at +6h, want the sliding refresh to re-issue it")
	}
	want := domain.FormatTime(e.clock.Now().Add(10 * time.Hour))
	if got := expiresAt(); got != want {
		t.Errorf("expiry after refresh = %s, want %s (now+TTL)", got, want)
	}

	// The refreshed session outlives the original expiry.
	e.clock.Advance(9 * time.Hour) // +15h from creation, 5h past the original 10h expiry
	status, body, _ := e.do(http.MethodGet, "/api/v1/auth/me", "", cookie, nil)
	if status != http.StatusOK {
		t.Errorf("me at +15h = %d %s, want 200 — the refresh did not stick", status, body)
	}
}

func TestSessionsSurviveRestart(t *testing.T) {
	e := newEnv(t, nil)
	cookie := e.setupOwner()

	// A new store handle over the same file and a new service: the process restarted.
	if err := e.st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	e.openStore()
	svc2 := auth.New(auth.Options{Store: e.st, Now: e.clock.Now})

	u, _, _, err := svc2.Authenticate(t.Context(), cookie.Value)
	if err != nil {
		t.Fatalf("Authenticate after restart: %v, want the cookie to still work", err)
	}
	if u.Email != "ada@example.com" {
		t.Errorf("user = %q, want ada@example.com", u.Email)
	}
}

// --------------------------------------------------------------- invites and roles -----

func TestInviteRedeemAndMemberLimits(t *testing.T) {
	e := newEnv(t, nil)
	owner := e.setupOwner()

	token := e.invite(owner)
	member := e.redeem(token, "theo@example.com")

	status, body, _ := e.do(http.MethodGet, "/api/v1/auth/me", "", member, nil)
	if status != http.StatusOK || !strings.Contains(body, `"role":"member"`) {
		t.Fatalf("member me = %d %s, want 200 with role member", status, body)
	}

	// One link, one member: the same token again is gone.
	status, body, _ = e.do(http.MethodPost, "/api/v1/auth/redeem",
		`{"token":"`+token+`","email":"again@example.com","display_name":"Again","password":"long enough"}`,
		nil, nil)
	wantProblem(t, status, body, http.StatusNotFound, "invite_invalid")

	// A member cannot mint invites: owner-only route answers 403.
	status, body, _ = e.do(http.MethodPost, "/api/v1/invites", "{}", member, nil)
	wantProblem(t, status, body, http.StatusForbidden, "forbidden")
}

func TestExpiredInviteIsGone(t *testing.T) {
	e := newEnv(t, func(o *auth.Options) { o.InviteTTL = 24 * time.Hour })
	owner := e.setupOwner()
	token := e.invite(owner)

	e.clock.Advance(25 * time.Hour)
	status, body, _ := e.do(http.MethodPost, "/api/v1/auth/redeem",
		`{"token":"`+token+`","email":"late@example.com","display_name":"Late","password":"long enough"}`,
		nil, nil)
	wantProblem(t, status, body, http.StatusGone, "invite_expired")
}

func TestRedeemWithATakenEmailConflicts(t *testing.T) {
	e := newEnv(t, nil)
	owner := e.setupOwner()
	token := e.invite(owner)

	status, body, _ := e.do(http.MethodPost, "/api/v1/auth/redeem",
		`{"token":"`+token+`","email":"ada@example.com","display_name":"Ada Again","password":"long enough"}`,
		nil, nil)
	wantProblem(t, status, body, http.StatusConflict, "email_taken")

	// The failed redemption rolled back: the invite is still redeemable.
	e.redeem(token, "fresh@example.com")
}

// ------------------------------------------------------------- project membership -----

func TestRequireProjectMember(t *testing.T) {
	e := newEnv(t, nil)
	owner := e.setupOwner()
	member := e.redeem(e.invite(owner), "theo@example.com")
	outsider := e.redeem(e.invite(owner), "eve@example.com")

	ownerUser, err := e.st.Users().ByEmail(t.Context(), "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	memberUser, err := e.st.Users().ByEmail(t.Context(), "theo@example.com")
	if err != nil {
		t.Fatal(err)
	}
	now := domain.FormatTime(e.clock.Now())
	project := domain.Project{ID: domain.NewID(), Key: "PAY", Name: "Payments", Color: "#ff8a3d",
		OwnerID: ownerUser.ID, CreatedAt: now, UpdatedAt: now}
	if err := e.st.Projects().Create(t.Context(), &project); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Projects().AddMember(t.Context(), project.ID, memberUser.ID); err != nil {
		t.Fatal(err)
	}

	// A project member passes; the workspace owner passes without a membership row.
	for name, cookie := range map[string]*http.Cookie{"member": member, "workspace owner": owner} {
		status, body, _ := e.do(http.MethodGet, "/api/v1/projects/PAY/probe", "", cookie, nil)
		if status != http.StatusOK {
			t.Errorf("%s on PAY = %d %s, want 200", name, status, body)
		}
	}

	// A workspace member without a membership row does not pass.
	status, body, _ := e.do(http.MethodGet, "/api/v1/projects/PAY/probe", "", outsider, nil)
	wantProblem(t, status, body, http.StatusForbidden, "forbidden")

	// An unknown project is a 404, member or not.
	status, body, _ = e.do(http.MethodGet, "/api/v1/projects/NOPE/probe", "", member, nil)
	wantProblem(t, status, body, http.StatusNotFound, "not_found")

	// No cookie at all never reaches the membership check.
	status, body, _ = e.do(http.MethodGet, "/api/v1/projects/PAY/probe", "", nil, nil)
	wantProblem(t, status, body, http.StatusUnauthorized, "unauthenticated")
}

// ------------------------------------------------------------------------- CSRF -----

func TestCSRF(t *testing.T) {
	e := newEnv(t, nil)
	cookie := e.setupOwner()

	// An unsafe request whose Origin names another host is refused, session or not.
	status, body, _ := e.do(http.MethodPost, "/api/v1/invites", "{}", cookie,
		map[string]string{"Origin": "http://evil.example"})
	wantProblem(t, status, body, http.StatusForbidden, "origin_forbidden")

	// The opaque "null" origin is refused too.
	status, body, _ = e.do(http.MethodPost, "/api/v1/invites", "{}", cookie,
		map[string]string{"Origin": "null"})
	wantProblem(t, status, body, http.StatusForbidden, "origin_forbidden")

	// A same-origin browser request passes.
	status, body, _ = e.do(http.MethodPost, "/api/v1/invites", "{}", cookie,
		map[string]string{"Origin": e.srv.URL})
	if status != http.StatusCreated {
		t.Errorf("same-origin POST = %d %s, want 201", status, body)
	}

	// No Origin but a cross-site fetch metadata header is refused.
	status, body, _ = e.do(http.MethodPost, "/api/v1/invites", "{}", cookie,
		map[string]string{"Sec-Fetch-Site": "cross-site"})
	wantProblem(t, status, body, http.StatusForbidden, "origin_forbidden")

	// A curl-style request — no Origin, no fetch metadata — passes.
	status, body, _ = e.do(http.MethodPost, "/api/v1/invites", "{}", cookie, nil)
	if status != http.StatusCreated {
		t.Errorf("headerless POST = %d %s, want 201 (non-browser clients are allowed)", status, body)
	}

	// Safe methods are exempt: a GET with a foreign Origin still answers.
	status, body, _ = e.do(http.MethodGet, "/api/v1/auth/me", "", cookie,
		map[string]string{"Origin": "http://evil.example"})
	if status != http.StatusOK {
		t.Errorf("GET with foreign Origin = %d %s, want 200 (CSRF checks unsafe methods only)", status, body)
	}
}

// ------------------------------------------------------------------- service level -----

func TestAuthenticateErrorVocabulary(t *testing.T) {
	e := newEnv(t, nil)
	e.setupOwner()

	if _, _, _, err := e.svc.Authenticate(t.Context(), ""); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("empty token err = %v, want ErrUnauthenticated", err)
	}
	if _, _, _, err := e.svc.Authenticate(t.Context(), "not-a-real-token"); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("garbage token err = %v, want ErrUnauthenticated", err)
	}
}

func TestValidationRejectsBadIdentity(t *testing.T) {
	e := newEnv(t, nil)

	for _, body := range []string{
		`{"email":"","display_name":"A","password":"long enough"}`,
		`{"email":"not-an-email","display_name":"A","password":"long enough"}`,
		`{"email":"a@b.c","display_name":"","password":"long enough"}`,
		`{"email":"a@b.c","display_name":"A","password":"short"}`,
		`this is not json`,
	} {
		status, resp, _ := e.do(http.MethodPost, "/api/v1/auth/setup", body, nil, nil)
		if status != http.StatusBadRequest || !strings.Contains(resp, `"invalid_request"`) {
			t.Errorf("setup with %s = %d %s, want 400 invalid_request", body, status, resp)
		}
	}
}
