//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunWritesSuccessfulEnvelope(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "codex-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "codex")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '{\"type\":\"agent_message\",\"text\":\"ok\"}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	exitCode := run([]string{"--codex-binary", binary, "--codex-home", home, "--scratch-parent", root}, bytes.NewBufferString(`{"prompt":"hello"}`), &stdout)
	if exitCode != 0 {
		t.Fatalf("exit=%d output=%s", exitCode, stdout.String())
	}
	var got response
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error != nil || got.Result.Output != "ok" || got.Result.AttemptID == "" {
		t.Fatalf("response=%+v", got)
	}
}

func TestRunRejectsUnknownRequestFields(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "codex-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "codex")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if exitCode := run([]string{"--codex-binary", binary, "--codex-home", home}, bytes.NewBufferString(`{"prompt":"hello","shell_args":["unsafe"]}`), &stdout); exitCode != 1 {
		t.Fatalf("exit=%d", exitCode)
	}
	var got response
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error == nil {
		t.Fatalf("response=%+v", got)
	}
}
