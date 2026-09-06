//go:build unix

package codexcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func fakeExecutor(t *testing.T, script string, mutate func(*Config)) *Executor {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "codex-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"auth_mode":"chatgpt"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "codex")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{BinaryPath: binary, CodexHome: home, ScratchParent: root, Timeout: 5 * time.Second}
	if mutate != nil {
		mutate(&cfg)
	}
	executor, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func TestRunCapturesStructuredResultAndUsage(t *testing.T) {
	executor := fakeExecutor(t, "cat >/dev/null\nprintf '%s\\n' '{\"type\":\"turn.completed\",\"model\":\"gpt-actual\",\"usage\":{\"input_tokens\":12,\"output_tokens\":3}}'\nprintf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"{\\\"ok\\\":true}\"}}'\n", nil)
	result, err := executor.Run(context.Background(), Request{Prompt: "return JSON", Model: "gpt-requested", ReasoningEffort: "high", SchemaName: "result", Schema: jsonObjectSchema()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != `{"ok":true}` || result.Model != "gpt-actual" || !result.Usage.Reported || result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 3 || result.AttemptID == "" || result.DurationMS < 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestRunReturnsPartialUsageAndSearchOnFailure(t *testing.T) {
	executor := fakeExecutor(t, "cat >/dev/null\nprintf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"web_search_call\",\"action\":{\"type\":\"search\",\"query\":\"one\"}}}'\nprintf '%s\\n' '{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":19,\"output_tokens\":4}}'\nexit 7\n", nil)
	result, err := executor.Run(context.Background(), Request{Prompt: "search", Tools: []Tool{ToolWebSearch}})
	if KindOf(err) != ProcessFailed || result.Tools.WebSearchCalls != 1 || result.Tools.InferredSearchQueries != 1 || !result.Usage.Reported || result.Usage.InputTokens != 19 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestUsageDistinguishesAbsentFromReportedZero(t *testing.T) {
	without := fakeExecutor(t, "cat >/dev/null\nprintf '%s\\n' '{\"type\":\"agent_message\",\"text\":\"ok\"}'\n", nil)
	result, err := without.Run(context.Background(), Request{Prompt: "ok"})
	if err != nil || result.Usage.Reported {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	withZero := fakeExecutor(t, "cat >/dev/null\nprintf '%s\\n' '{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}'\nprintf '%s\\n' '{\"type\":\"agent_message\",\"text\":\"ok\"}'\n", nil)
	result, err = withZero.Run(context.Background(), Request{Prompt: "ok"})
	if err != nil || !result.Usage.Reported || result.Usage.InputTokens != 0 || result.Usage.OutputTokens != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestCancelledBeforeStartDoesNotRunProcess(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "ran")
	executor := fakeExecutor(t, fmt.Sprintf("touch %q\n", marker), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := executor.Run(ctx, Request{Prompt: "no"})
	if KindOf(err) != Cancelled || !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("process ran: %v", statErr)
	}
}

func TestProcessEnvironmentIsAllowlistedAndScratchIsRemoved(t *testing.T) {
	root := t.TempDir()
	envLog := filepath.Join(root, "env.log")
	executor := fakeExecutor(t, fmt.Sprintf("env > %q\ncat >/dev/null\nprintf '%%s\\n' '{\"type\":\"agent_message\",\"text\":\"ok\"}'\n", envLog), nil)
	t.Setenv("CODEX_PROVIDER_SECRET_MARKER", "must-not-leak")
	if _, err := executor.Run(context.Background(), Request{Prompt: "ok"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "must-not-leak") || strings.Contains(string(raw), "CODEX_PROVIDER_SECRET_MARKER") {
		t.Fatalf("environment leaked: %s", raw)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "codex-cli-provider-") {
			t.Fatalf("scratch retained: %s", entry.Name())
		}
	}
}

func TestAuthenticationAndRateLimitClassificationDoesNotExposeStderr(t *testing.T) {
	for _, test := range []struct {
		name, stderr string
		kind         ErrorKind
	}{
		{"auth", "login required secret-token", AuthenticationFailed},
		{"rate", "HTTP 429 secret-token", RateLimited},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor := fakeExecutor(t, fmt.Sprintf("cat >/dev/null\nprintf %q >&2\nexit 1\n", test.stderr), nil)
			_, err := executor.Run(context.Background(), Request{Prompt: "ok"})
			if KindOf(err) != test.kind || strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("kind=%q err=%v", KindOf(err), err)
			}
		})
	}
}

func TestDefaultLogsDoNotContainPromptStderrOrPaths(t *testing.T) {
	var logs bytes.Buffer
	executor := fakeExecutor(t, "cat >/dev/null\nprintf 'secret-stderr\\n' >&2\nprintf '%s\\n' '{\"type\":\"agent_message\",\"text\":\"secret-output\"}'\n", func(cfg *Config) {
		cfg.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	if _, err := executor.Run(context.Background(), Request{Prompt: "secret-prompt"}); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-prompt", "secret-stderr", "secret-output", executor.codexHome, executor.scratchParent} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("logs exposed %q: %s", secret, logs.String())
		}
	}
}

func TestRunClassifiesTimeoutAndRetainsPartialSearch(t *testing.T) {
	executor := fakeExecutor(t, "cat >/dev/null\nprintf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"web_search_call\",\"action\":{\"type\":\"search\",\"query\":\"one\"}}}'\nsleep 10\n", func(cfg *Config) { cfg.Timeout = 100 * time.Millisecond })
	result, err := executor.Run(context.Background(), Request{Prompt: "search", Tools: []Tool{ToolWebSearch}})
	if KindOf(err) != TimedOut || !errors.Is(err, context.DeadlineExceeded) || result.Tools.WebSearchCalls != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestCancellationTerminatesDescendantProcess(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "codex-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "child.pid")
	binary := filepath.Join(root, "codex")
	script := "#!/bin/sh\nsleep 30 &\nchild=$!\nprintf '%s' \"$child\" > '" + marker + "'\nwait \"$child\"\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	executor, err := New(Config{BinaryPath: binary, CodexHome: home, ScratchParent: root})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for i := 0; i < 100; i++ {
			if _, err := os.Stat(marker); err == nil {
				cancel()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	result, err := executor.Run(ctx, Request{Prompt: "ignored"})
	if KindOf(err) != Cancelled || result.OrphanProcesses != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertProcessGone(t, marker)
}

func TestSuccessfulDirectExitTerminatesDescendantProcess(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "codex-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "child.pid")
	binary := filepath.Join(root, "codex")
	script := "#!/bin/sh\nsleep 30 &\nchild=$!\nprintf '%s' \"$child\" > '" + marker + "'\nprintf '%s\\n' '{\"type\":\"agent_message\",\"text\":\"ok\"}'\nexit 0\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	executor, err := New(Config{BinaryPath: binary, CodexHome: home, ScratchParent: root})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Run(context.Background(), Request{Prompt: "ignored"})
	if err != nil || result.OrphanProcesses != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertProcessGone(t, marker)
}

func assertProcessGone(t *testing.T, marker string) {
	t.Helper()
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &pid); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("descendant pid %d survived: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunRejectsUnsupportedToolBeforeProcess(t *testing.T) {
	executor := fakeExecutor(t, "exit 99\n", nil)
	_, err := executor.Run(context.Background(), Request{Prompt: "x", Tools: []Tool{"shell"}})
	if KindOf(err) != UnsupportedCapability {
		t.Fatalf("err=%v", err)
	}
}

func TestReadLimitedDrainsBeyondRetention(t *testing.T) {
	reader := bytes.NewReader(bytes.Repeat([]byte("x"), 4096))
	ch := make(chan limitedRead, 1)
	readLimited(reader, 128, ch)
	result := <-ch
	if result.err != nil || !result.tooLarge || len(result.data) != 128 || reader.Len() != 0 {
		t.Fatalf("result=%+v remaining=%d", result, reader.Len())
	}
}

func TestSharedLimiterSerializesExecutors(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "codex-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "processes.log")
	binary := filepath.Join(root, "codex")
	script := fmt.Sprintf("#!/bin/sh\nprintf 'start\\n' >> %q\nsleep 0.1\ncat >/dev/null\nprintf '%%s\\n' '{\"type\":\"agent_message\",\"text\":\"ok\"}'\nprintf 'end\\n' >> %q\n", logPath, logPath)
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	limiter := NewLimiter(1)
	executors := make([]*Executor, 2)
	for i := range executors {
		var err error
		executors[i], err = New(Config{BinaryPath: binary, CodexHome: home, ScratchParent: root, Limiter: limiter})
		if err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, executor := range executors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := executor.Run(context.Background(), Request{Prompt: "ok"})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(raw)); !slices.Equal(got, []string{"start", "end", "start", "end"}) {
		t.Fatalf("overlap: %q", raw)
	}
}

func TestLimiterWaitIsCancelable(t *testing.T) {
	limiter := NewLimiter(1)
	release, err := limiter.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := limiter.acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestExecArgsAreRestrictive(t *testing.T) {
	joined := strings.Join(execArgs(Request{Model: "gpt-test", ReasoningEffort: "high"}, false), " ")
	for _, want := range []string{"--ignore-user-config", "approval_policy=\"never\"", "sandbox_mode=\"read-only\"", "features.shell_tool=false", "features.multi_agent=false", "tools.web_search=false"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "--ignore-rules") {
		t.Fatalf("exec-policy rules were disabled: %s", joined)
	}
}

func jsonObjectSchema() []byte { return []byte(`{"type":"object","additionalProperties":false}`) }
