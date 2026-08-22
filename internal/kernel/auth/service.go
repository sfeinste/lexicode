package auth

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Defaults. Both are policy documented in doc.go; Options overrides them only in tests.
const (
	// DefaultSessionTTL is how long a session lives without activity. With sliding refresh an
	// active user never expires; an absent one expires this long after their last request.
	DefaultSessionTTL = 30 * 24 * time.Hour
	// DefaultInviteTTL is how long an invite link stays redeemable.
	DefaultInviteTTL = 7 * 24 * time.Hour
)

// minPasswordLen is the whole password policy: length only, no composition rules.
const minPasswordLen = 8

// The service's error vocabulary. The HTTP layer maps each to a status and a problem type slug;
// callers elsewhere match with errors.Is.
var (
	// ErrAlreadySetup is returned by Setup once any user exists (409, "already_setup").
	ErrAlreadySetup = errors.New("setup already completed: a user exists")
	// ErrInvalidCredentials is a failed login — unknown email, wrong password and archived
	// user are deliberately indistinguishable (401, "invalid_credentials").
	ErrInvalidCredentials = errors.New("invalid email or password")
	// ErrUnauthenticated is a missing, unknown or revoked session token (401, "unauthenticated").
	ErrUnauthenticated = errors.New("not authenticated")
	// ErrSessionExpired is a session past its expiry (401, "session_expired"). It is distinct
	// from ErrUnauthenticated so the SPA can say "you were signed out" instead of "sign in".
	ErrSessionExpired = errors.New("session expired")
	// ErrInviteInvalid is an unknown or already-redeemed invite token (404, "invite_invalid").
	ErrInviteInvalid = errors.New("invite is invalid or was already used")
	// ErrInviteExpired is an invite past its expiry (410, "invite_expired").
	ErrInviteExpired = errors.New("invite has expired")
)

// ValidationError is a bad input the caller can fix (400, "invalid_request"). Detail is safe to
// show to the user.
type ValidationError struct{ Detail string }

func (e *ValidationError) Error() string { return e.Detail }

func invalid(format string, args ...any) error {
	return &ValidationError{Detail: fmt.Sprintf(format, args...)}
}

// Service is the identity service: everything auth-shaped goes through here. It is stateless
// apart from a cache of "at least one user exists", so any number of handles over the same
// store agree with each other.
type Service struct {
	st         *store.Store
	logger     *slog.Logger
	now        func() time.Time
	sessionTTL time.Duration
	inviteTTL  time.Duration

	// hasUsers caches the setup gate's question. False means "ask the database"; true is
	// final — users are never hard-deleted, so a workspace never returns to first-run.
	hasUsers atomic.Bool
}

// Options configures New. Store is required; everything else has a default.
type Options struct {
	Store  *store.Store
	Logger *slog.Logger
	// Now is the clock, injectable so tests can move time. Nil means time.Now.
	Now func() time.Time
	// SessionTTL and InviteTTL override the defaults; zero means the default.
	SessionTTL time.Duration
	InviteTTL  time.Duration
}

// New builds the service.
func New(opts Options) *Service {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	sessionTTL := opts.SessionTTL
	if sessionTTL <= 0 {
		sessionTTL = DefaultSessionTTL
	}
	inviteTTL := opts.InviteTTL
	if inviteTTL <= 0 {
		inviteTTL = DefaultInviteTTL
	}
	return &Service{st: opts.Store, logger: logger, now: now,
		sessionTTL: sessionTTL, inviteTTL: inviteTTL}
}

// SetupDone reports whether the workspace has any user — the question the setup gate asks on
// every request, answered from the cache once it has ever been true.
func (s *Service) SetupDone(ctx context.Context) (bool, error) {
	if s.hasUsers.Load() {
		return true, nil
	}
	n, err := s.st.Users().Count(ctx)
	if err != nil {
		return false, err
	}
	if n > 0 {
		s.hasUsers.Store(true)
		return true, nil
	}
	return false, nil
}

// Setup creates the owner on a zero-user database. Once any user exists it returns
// ErrAlreadySetup, forever; the existence check and the insert share one transaction so two
// racing setups cannot both win.
func (s *Service) Setup(ctx context.Context, email, displayName, password string) (domain.User, error) {
	email, displayName, err := normalizeIdentity(email, displayName, password)
	if err != nil {
		return domain.User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return domain.User{}, err
	}
	u := s.newUser(email, displayName, hash, domain.RoleOwner)
	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		n, err := tx.Users().Count(ctx)
		if err != nil {
			return err
		}
		if n > 0 {
			return ErrAlreadySetup
		}
		return tx.Users().Create(ctx, &u)
	})
	if err != nil {
		return domain.User{}, err
	}
	s.hasUsers.Store(true)
	return u, nil
}

// Login checks a password and returns the user. Unknown email, wrong password, archived user
// and an unloggable fixture hash all return ErrInvalidCredentials.
func (s *Service) Login(ctx context.Context, email, password string) (domain.User, error) {
	u, err := s.st.Users().ByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if errors.Is(err, store.ErrNotFound) {
		// Burn roughly the same time as a real verification so that response timing does not
		// reveal which emails exist.
		_, _ = VerifyPassword(timingDecoy(), password)
		return domain.User{}, ErrInvalidCredentials
	}
	if err != nil {
		return domain.User{}, err
	}
	ok, err := VerifyPassword(u.PasswordHash, password)
	if errors.Is(err, ErrBadHash) {
		return domain.User{}, ErrInvalidCredentials
	}
	if err != nil {
		return domain.User{}, err
	}
	if !ok || u.ArchivedAt != nil {
		return domain.User{}, ErrInvalidCredentials
	}
	return u, nil
}

// CreateSession mints a session for the user and returns the raw token the cookie will carry.
// Only the token's hash is stored. Expired rows are swept opportunistically, best-effort.
func (s *Service) CreateSession(ctx context.Context, userID, userAgent string) (string, domain.Session, error) {
	token, err := NewToken()
	if err != nil {
		return "", domain.Session{}, err
	}
	now := s.now()
	sess := domain.Session{
		ID:        HashToken(token),
		UserID:    userID,
		ExpiresAt: domain.FormatTime(now.Add(s.sessionTTL)),
		CreatedAt: domain.FormatTime(now),
	}
	if userAgent != "" {
		sess.UserAgent = &userAgent
	}
	if err := s.st.Sessions().Create(ctx, &sess); err != nil {
		return "", domain.Session{}, err
	}
	if n, err := s.st.Sessions().DeleteExpired(ctx, domain.FormatTime(now)); err != nil {
		s.logger.Warn("could not sweep expired sessions", slog.String("error", err.Error()))
	} else if n > 0 {
		s.logger.Debug("swept expired sessions", slog.Int64("count", n))
	}
	return token, sess, nil
}

// Authenticate resolves a raw session token to its user, applying sliding refresh: once a
// session's remaining lifetime has fallen below half the TTL, the expiry moves to now+TTL.
// refreshed reports whether that happened, so the HTTP layer can re-issue the cookie.
func (s *Service) Authenticate(ctx context.Context, token string) (u domain.User, sess domain.Session, refreshed bool, err error) {
	if token == "" {
		return domain.User{}, domain.Session{}, false, ErrUnauthenticated
	}
	sess, err = s.st.Sessions().ByID(ctx, HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		// Unknown and revoked (logged-out) tokens are the same thing: no session row.
		return domain.User{}, domain.Session{}, false, ErrUnauthenticated
	}
	if err != nil {
		return domain.User{}, domain.Session{}, false, err
	}
	expires, err := domain.ParseTime(sess.ExpiresAt)
	if err != nil {
		return domain.User{}, domain.Session{}, false, err
	}
	now := s.now()
	if !now.Before(expires) {
		return domain.User{}, domain.Session{}, false, ErrSessionExpired
	}
	u, err = s.st.Users().ByID(ctx, sess.UserID)
	if err != nil {
		return domain.User{}, domain.Session{}, false, err
	}
	if u.ArchivedAt != nil {
		return domain.User{}, domain.Session{}, false, ErrUnauthenticated
	}
	if expires.Sub(now) < s.sessionTTL/2 {
		sess.ExpiresAt = domain.FormatTime(now.Add(s.sessionTTL))
		if err := s.st.Sessions().Extend(ctx, sess.ID, sess.ExpiresAt); err != nil {
			// The session is still valid as stored; a failed refresh must not fail the request.
			s.logger.Warn("could not extend session", slog.String("error", err.Error()))
		} else {
			refreshed = true
		}
	}
	return u, sess, refreshed, nil
}

// Logout revokes the session this token names. Idempotent: an unknown token is a no-op.
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.st.Sessions().Delete(ctx, HashToken(token))
}

// RevokeSessions deletes every session of one user — the S37 owner action for a lost laptop
// or a departing teammate. Returns how many sessions were live. The user must exist
// (ErrNotFound otherwise); revoking a signed-out user succeeds with 0. The kernel's route
// wrapper writes the audit entry — this package cannot import the audit writer (it would
// cycle: audit reads actors from this package).
func (s *Service) RevokeSessions(ctx context.Context, userID string) (int64, error) {
	if _, err := s.st.Users().ByID(ctx, userID); err != nil {
		return 0, err
	}
	return s.st.Sessions().DeleteForUser(ctx, userID)
}

// CreateInvite mints a one-time member invite and returns the URL path carrying the raw token.
// Only the token's hash is stored.
func (s *Service) CreateInvite(ctx context.Context, createdBy string) (string, domain.Invite, error) {
	token, err := NewToken()
	if err != nil {
		return "", domain.Invite{}, err
	}
	now := s.now()
	inv := domain.Invite{
		ID:        domain.NewID(),
		TokenHash: HashToken(token),
		Role:      domain.RoleMember,
		CreatedBy: createdBy,
		ExpiresAt: domain.FormatTime(now.Add(s.inviteTTL)),
		CreatedAt: domain.FormatTime(now),
	}
	if err := s.st.Invites().Create(ctx, &inv); err != nil {
		return "", domain.Invite{}, err
	}
	return "/invite/" + token, inv, nil
}

// Redeem consumes an invite token and creates the member it invited. The token is single-use:
// the lookup, the user insert and the redeemed_by mark share one transaction, and marking an
// already-redeemed invite fails, so two racing redemptions cannot both win.
func (s *Service) Redeem(ctx context.Context, token, email, displayName, password string) (domain.User, error) {
	email, displayName, err := normalizeIdentity(email, displayName, password)
	if err != nil {
		return domain.User{}, err
	}
	// Hash outside the transaction: ~50ms of argon2 must not hold the write lock.
	hash, err := HashPassword(password)
	if err != nil {
		return domain.User{}, err
	}
	var u domain.User
	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		inv, err := tx.Invites().ByTokenHash(ctx, HashToken(token))
		if errors.Is(err, store.ErrNotFound) {
			return ErrInviteInvalid
		}
		if err != nil {
			return err
		}
		if inv.RedeemedBy != nil {
			return ErrInviteInvalid
		}
		expires, err := domain.ParseTime(inv.ExpiresAt)
		if err != nil {
			return err
		}
		if !s.now().Before(expires) {
			return ErrInviteExpired
		}
		u = s.newUser(email, displayName, hash, inv.Role)
		if err := tx.Users().Create(ctx, &u); err != nil {
			return err
		}
		if err := tx.Invites().Redeem(ctx, inv.ID, u.ID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return ErrInviteInvalid // raced with another redemption
			}
			return err
		}
		return nil
	})
	if err != nil {
		return domain.User{}, err
	}
	s.hasUsers.Store(true)
	return u, nil
}

// IsProjectMember answers RequireProjectMember's question: workspace owners and the project's
// owner see every project; everyone else needs a project_members row.
func (s *Service) IsProjectMember(ctx context.Context, u domain.User, p domain.Project) (bool, error) {
	if u.Role == domain.RoleOwner || p.OwnerID == u.ID {
		return true, nil
	}
	ids, err := s.st.Projects().MemberIDs(ctx, p.ID)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if id == u.ID {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) newUser(email, displayName, hash string, role domain.UserRole) domain.User {
	return domain.User{
		ID:           domain.NewID(),
		Email:        email,
		DisplayName:  displayName,
		PasswordHash: hash,
		Role:         role,
		AvatarColor:  avatarColor(email),
		CreatedAt:    domain.FormatTime(s.now()),
	}
}

// normalizeIdentity trims and lowercases the email and validates all three identity inputs,
// returning a ValidationError the HTTP layer renders as a 400.
func normalizeIdentity(email, displayName, password string) (string, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	displayName = strings.TrimSpace(displayName)
	switch {
	case email == "" || !strings.Contains(email, "@") || strings.ContainsAny(email, " \t"):
		return "", "", invalid("a valid email address is required")
	case displayName == "":
		return "", "", invalid("a display name is required")
	case len(password) < minPasswordLen:
		return "", "", invalid("the password must be at least %d characters", minPasswordLen)
	}
	return email, displayName, nil
}

// avatarPalette matches the accent colours the seed fixtures use; the pick is a stable hash of
// the email so a user keeps their colour across reinstalls.
var avatarPalette = []string{
	"#7c5cff", "#00a884", "#ff8a3d", "#3d9bff", "#e5484d", "#f5b83d", "#c060c0", "#5cb85c",
}

func avatarColor(email string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(email))
	return avatarPalette[h.Sum32()%uint32(len(avatarPalette))]
}

// timingDecoy returns a well-formed hash to verify against when the email does not exist, so
// both login failure paths cost about the same argon2 time. Lazily built once, on the first
// unknown-email login, rather than at package init where every test binary would pay for it.
var timingDecoy = sync.OnceValue(func() string {
	h, err := HashPassword("timing-decoy-not-a-real-account")
	if err != nil {
		// crypto/rand failing is a broken platform; an empty string just means the decoy
		// verification returns ErrBadHash fast, which is the pre-decoy behaviour.
		return ""
	}
	return h
})
