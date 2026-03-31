package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMainCallsRunMain(t *testing.T) {
	called := false
	original := runMain
	t.Cleanup(func() { runMain = original })

	runMain = func() error {
		called = true
		return nil
	}

	main()

	if !called {
		t.Fatal("expected main to call runMain")
	}
}

func TestMainPrintsErrors(t *testing.T) {
	original := runMain
	originalStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe returned error: %v", err)
	}
	t.Cleanup(func() {
		runMain = original
		os.Stderr = originalStderr
		_ = reader.Close()
	})

	runMain = func() error {
		return errors.New("boom")
	}
	os.Stderr = writer

	main()

	_ = writer.Close()
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	if !strings.Contains(string(output), "boom") {
		t.Fatalf("expected stderr to contain the error, got %q", string(output))
	}
}

func setRunMainEnv(t *testing.T, dataDir string) {
	t.Helper()
	t.Setenv("SYNCTIMETHING_LISTEN_ADDR", "127.0.0.1:0")
	t.Setenv("SYNCTIMETHING_DATA_DIR", dataDir)
	t.Setenv("SYNCTIMETHING_DB_PATH", filepath.Join(dataDir, "app.db"))
	t.Setenv("SYNCTIMETHING_SESSION_COOKIE", "test-session")
	t.Setenv("SYNCTIMETHING_SESSION_TTL", "1h")
	t.Setenv("SYNCTIMETHING_SECURE_COOKIES", "false")
	t.Setenv("SYNCTIMETHING_ADMIN_USERNAME", "admin")
	t.Setenv("SYNCTIMETHING_ADMIN_PASSWORD", "secret")
	t.Setenv("SYNCTIMETHING_TIMEZONE", "UTC")
}

func TestRunMainConfigError(t *testing.T) {
	setRunMainEnv(t, t.TempDir())
	t.Setenv("SYNCTIMETHING_SESSION_TTL", "bad")

	err := runMain()
	if err == nil || !strings.Contains(err.Error(), "SYNCTIMETHING_SESSION_TTL") {
		t.Fatalf("unexpected runMain error: %v", err)
	}
}

func TestRunMainServeError(t *testing.T) {
	setRunMainEnv(t, t.TempDir())
	t.Setenv("SYNCTIMETHING_LISTEN_ADDR", "127.0.0.1")

	err := runMain()
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("unexpected runMain error: %v", err)
	}
}

func TestRunMainAppCreationError(t *testing.T) {
	setRunMainEnv(t, t.TempDir())
	t.Setenv("SYNCTIMETHING_ADMIN_PASSWORD", "")

	err := runMain()
	if err == nil || !strings.Contains(err.Error(), "first boot") {
		t.Fatalf("unexpected runMain error: %v", err)
	}
}
