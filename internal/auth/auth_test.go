package auth

import (
	"errors"
	"strings"
	"testing"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("boom")
}

func TestHashAndComparePassword(t *testing.T) {
	hash, err := HashPassword("secret-password")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	if err := ComparePassword(hash, "secret-password"); err != nil {
		t.Fatalf("ComparePassword returned error: %v", err)
	}
	if err := ComparePassword(hash, "wrong"); err == nil {
		t.Fatal("expected compare error for wrong password")
	}
}

func TestHashPasswordRejectsBlank(t *testing.T) {
	_, err := HashPassword("   ")
	if err == nil || !strings.Contains(err.Error(), "password cannot be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHashPasswordWrapsBcryptErrors(t *testing.T) {
	_, err := HashPassword(strings.Repeat("a", 73))
	if err == nil || !strings.Contains(err.Error(), "hash password") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewSessionToken(t *testing.T) {
	plain, hashed, err := NewSessionToken(strings.NewReader(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatalf("NewSessionToken returned error: %v", err)
	}
	if plain == "" || hashed == "" {
		t.Fatal("expected non-empty token output")
	}
	if HashToken(plain) != hashed {
		t.Fatal("expected hash to match plain token")
	}
}

func TestNewSessionTokenError(t *testing.T) {
	_, _, err := NewSessionToken(failingReader{})
	if err == nil || !strings.Contains(err.Error(), "read session entropy") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewSessionTokenUsesDefaultReader(t *testing.T) {
	plain, hashed, err := NewSessionToken(nil)
	if err != nil {
		t.Fatalf("NewSessionToken returned error: %v", err)
	}
	if plain == "" || hashed == "" {
		t.Fatal("expected token output when using the default entropy source")
	}
}
