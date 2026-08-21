package auth_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/spruce/lexicode/internal/kernel/auth"
)

func TestPasswordHashRoundtrip(t *testing.T) {
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	// The parameters live in the hash (PHC format), so tuning them later never invalidates
	// existing rows. This prefix is the documented t=1, m=64MB, p=4 shape.
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=1,p=4$") {
		t.Errorf("hash = %q, want the documented argon2id parameters encoded in it", hash)
	}

	ok, err := auth.VerifyPassword(hash, "correct horse battery staple")
	if err != nil || !ok {
		t.Errorf("verify with the right password = (%v, %v), want (true, nil)", ok, err)
	}
	ok, err = auth.VerifyPassword(hash, "wrong horse")
	if err != nil || ok {
		t.Errorf("verify with the wrong password = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestTwoHashesOfOnePasswordDiffer(t *testing.T) {
	a, err := auth.HashPassword("hunter22")
	if err != nil {
		t.Fatal(err)
	}
	b, err := auth.HashPassword("hunter22")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two hashes of the same password are identical; the salt is not random")
	}
}

func TestMalformedStoredHashIsAnError(t *testing.T) {
	// "!seed-fixture-no-login" is what the seed fixtures store, deliberately unloggable.
	for _, h := range []string{"", "!seed-fixture-no-login", "$argon2id$v=19$m=0,t=0,p=0$x$y",
		"$argon2i$v=19$m=65536,t=1,p=4$AAAA$AAAA"} {
		if _, err := auth.VerifyPassword(h, "anything"); !errors.Is(err, auth.ErrBadHash) {
			t.Errorf("VerifyPassword(%q) err = %v, want ErrBadHash", h, err)
		}
	}
}

func TestTokenHashIsStableAndOpaque(t *testing.T) {
	tok, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 43 { // 32 bytes, base64url, no padding
		t.Errorf("token length = %d, want 43", len(tok))
	}
	first, second := auth.HashToken(tok), auth.HashToken(tok)
	if first != second {
		t.Error("HashToken is not deterministic")
	}
	if first == tok || len(first) != 64 {
		t.Errorf("HashToken(%q) = %q, want a 64-char hex digest distinct from the token", tok, first)
	}
}
