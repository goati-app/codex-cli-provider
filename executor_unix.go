//go:build unix

package codexcli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const debugPayloadMaxBytes = 64 << 10

type Executor struct {
	binary           string
	codexHome        string
	scratchParent    string
	timeout          time.Duration
	jsonlMaxBytes    int64
	resultMaxBytes   int64
	stderrMaxBytes   int64
	metadataMaxBytes int
	limiter          *Limiter
	logger           *slog.Logger
	logName          string
	debugPayloads    bool
}

func New(cfg Config) (*Executor, error) {
	binary := strings.TrimSpace(cfg.BinaryPath)
	if binary == "" {
		binary = "codex"
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return nil, newError(InvalidConfig, "resolve executable", err)
	}
	home := strings.TrimSpace(cfg.CodexHome)
	if home == "" {
		return nil, newError(InvalidConfig, "configure CODEX_HOME", nil)
	}
	home, err = filepath.Abs(home)
	if err != nil {
		return nil, newError(InvalidConfig, "configure CODEX_HOME", err)
	}
	scratch := strings.TrimSpace(cfg.ScratchParent)
	if scratch == "" {
		scratch = os.TempDir()
	}
	scratch, err = filepath.Abs(scratch)
	if err != nil {
		return nil, newError(InvalidConfig, "configure scratch parent", err)
	}
	if info, statErr := os.Stat(scratch); statErr != nil || !info.IsDir() {
		return nil, newError(InvalidConfig, "configure scratch parent", statErr)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	jsonlMax := cfg.JSONLMaxBytes
	if jsonlMax <= 0 {
		jsonlMax = DefaultJSONLMaxBytes
	}
	resultMax := cfg.ResultMaxBytes
	if resultMax <= 0 {
		resultMax = DefaultResultMaxBytes
	}
	stderrMax := cfg.StderrMaxBytes
	if stderrMax <= 0 {
		stderrMax = DefaultStderrMaxBytes
	}
	metadataMax := cfg.MetadataMaxBytes
	if metadataMax <= 0 {
		metadataMax = DefaultMetadataMaxBytes
	}
	if jsonlMax > DefaultJSONLMaxBytes || resultMax > DefaultResultMaxBytes || stderrMax > DefaultStderrMaxBytes || metadataMax > DefaultMetadataMaxBytes {
		return nil, newError(InvalidConfig, "configure retention limits", fmt.Errorf("limit exceeds safety ceiling"))
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logName := strings.TrimSpace(cfg.LogName)
	if logName == "" {
		logName = "codex cli"
	}
	return &Executor{binary: resolved, codexHome: home, scratchParent: scratch, timeout: timeout, jsonlMaxBytes: jsonlMax, resultMaxBytes: resultMax, stderrMaxBytes: stderrMax, metadataMaxBytes: metadataMax, limiter: cfg.Limiter, logger: logger, logName: logName, debugPayloads: cfg.DebugPayloads}, nil
}

func (e *Executor) IsConfigured() bool {
	if e == nil || e.binary == "" || e.codexHome == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(e.codexHome, "auth.json"))
	return err == nil && !info.IsDir()
}

func (e *Executor) Run(ctx context.Context, req Request) (result Result, runErr error) {
	started := time.Now()
	result.AttemptID = newAttemptID()
	result.Model = strings.TrimSpace(req.Model)
	defer func() {
		result.Duration = time.Since(started)
		result.DurationMS = result.Duration.Milliseconds()
	}()
	if ctx == nil {
		return result, newError(InvalidConfig, "validate request", fmt.Errorf("nil context"))
	}
	if err := ctx.Err(); err != nil {
		return result, contextError("before start", err)
	}
	webSearch, err := validateRequest(req)
	if err != nil {
		return result, err
	}
	if e == nil {
		return result, newError(InvalidConfig, "validate executor", nil)
	}
	if !e.IsConfigured() {
		return result, newError(InvalidConfig, "validate authentication", nil)
	}
	e.logger.Debug(e.logName+" attempt queued", "attempt_id", result.AttemptID, "model", result.Model, "reasoning_effort", req.ReasoningEffort, "web_search", webSearch, "timeout_ms", e.timeout.Milliseconds())
	attemptCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	queueStarted := time.Now()
	release, err := e.limiter.acquire(attemptCtx)
	result.QueueDuration = time.Since(queueStarted)
	result.QueueMS = result.QueueDuration.Milliseconds()
	if err != nil {
		return result, contextError("wait for capacity", err)
	}
	defer release()
	e.logger.Debug(e.logName+" process slot acquired", "attempt_id", result.AttemptID, "queue_ms", result.QueueMS)
	scratch, err := os.MkdirTemp(e.scratchParent, "codex-cli-provider-")
	if err != nil {
		return result, newError(InvalidConfig, "create scratch directory", err)
	}
	defer os.RemoveAll(scratch)
	if err := os.Chmod(scratch, 0o700); err != nil {
		return result, newError(InvalidConfig, "secure scratch directory", err)
	}
	for _, dir := range []string{filepath.Join(scratch, "home"), filepath.Join(scratch, "tmp")} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return result, newError(InvalidConfig, "create private directory", err)
		}
	}
	args := execArgs(req, webSearch)
	if len(req.Schema) > 0 {
		schemaPath := filepath.Join(scratch, "output-schema.json")
		if err := os.WriteFile(schemaPath, req.Schema, 0o600); err != nil {
			return result, newError(InvalidConfig, "write output schema", err)
		}
		args = append(args, "--output-schema", schemaPath)
	}
	args = append(args, "--skip-git-repo-check", "--cd", scratch, "-")
	cmd := exec.Command(e.binary, args...)
	cmd.Dir = scratch
	cmd.Env = []string{
		"CODEX_HOME=" + e.codexHome,
		"HOME=" + filepath.Join(scratch, "home"),
		"TMPDIR=" + filepath.Join(scratch, "tmp"),
		"PATH=" + RuntimePATH(e.binary),
		"LANG=C.UTF-8", "LC_ALL=C.UTF-8",
	}
	cmd.Stdin = strings.NewReader(req.Prompt)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return result, newError(StartFailed, "open stdout", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return result, newError(StartFailed, "open stderr", err)
	}
	if err := cmd.Start(); err != nil {
		return result, classifyStartError(err)
	}
	defer stdout.Close()
	defer stderr.Close()
	outCh := make(chan limitedRead, 1)
	errCh := make(chan limitedRead, 1)
	go readLimited(stdout, e.jsonlMaxBytes, outCh)
	go readLimited(stderr, e.stderrMaxBytes, errCh)
	waitCh := make(chan error, 1)
	go func() { waitCh <- waitProcess(cmd.Process) }()
	var waitErr error
	var groupGone bool
	select {
	case waitErr = <-waitCh:
		groupGone = terminateProcessGroup(cmd.Process.Pid)
	case <-attemptCtx.Done():
		groupGone = terminateProcessGroup(cmd.Process.Pid)
		waitErr = <-waitCh
	}
	if !groupGone {
		result.OrphanProcesses = 1
	}
	out := awaitPipe(outCh, stdout)
	errOut := awaitPipe(errCh, stderr)
	result.StdoutTruncated = out.tooLarge
	result.StderrTruncated = errOut.tooLarge
	parsed, parseErr := ParseJSONL(out.data, e.jsonlMaxBytes, e.resultMaxBytes, e.metadataMaxBytes)
	applyProtocolResult(&result, parsed)
	if e.debugPayloads {
		outDebug, outTruncated := debugPayload(out.data)
		errDebug, errTruncated := debugPayload(errOut.data)
		e.logger.Debug(e.logName+" raw diagnostic payload", "attempt_id", result.AttemptID, "stdout_jsonl", outDebug, "stderr", errDebug, "stdout_truncated", outTruncated, "stderr_truncated", errTruncated)
	}
	e.logger.Debug(e.logName+" process finished", "attempt_id", result.AttemptID, "events", result.Events, "tokens_reported", result.Usage.Reported, "web_search_calls", result.Tools.WebSearchCalls, "stdout_bytes", len(out.data), "stderr_bytes", len(errOut.data), "stdout_too_large", out.tooLarge, "stderr_too_large", errOut.tooLarge)
	if attemptCtx.Err() != nil {
		return result, contextError("execute", attemptCtx.Err())
	}
	if out.err != nil || errOut.err != nil || out.tooLarge || errOut.tooLarge {
		return result, newError(ProtocolInvalid, "read bounded output", firstError(out.err, errOut.err))
	}
	if !groupGone {
		return result, newError(ProtocolInvalid, "terminate descendants", nil)
	}
	if waitErr != nil {
		return result, classifyProcessError(waitErr, errOut.data)
	}
	if parseErr != nil {
		return result, parseErr
	}
	e.logger.Debug(e.logName+" protocol parsed", "attempt_id", result.AttemptID, "model", result.Model, "events", result.Events, "official_web_search_calls", result.Tools.WebSearchCalls, "inferred_web_search_queries", result.Tools.InferredSearchQueries, "tokens_reported", result.Usage.Reported, "input_tokens", result.Usage.InputTokens, "output_tokens", result.Usage.OutputTokens)
	if webSearch {
		maxCalls := req.MaxWebSearchCalls
		if maxCalls == 0 {
			maxCalls = 3
		}
		if err := ValidateWebSearchResult(parsed, maxCalls); err != nil {
			return result, err
		}
	}
	return result, nil
}

func validateRequest(req Request) (bool, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return false, newError(InvalidConfig, "validate request", fmt.Errorf("prompt is empty"))
	}
	if req.MaxWebSearchCalls < 0 {
		return false, newError(InvalidConfig, "validate request", fmt.Errorf("negative web search limit"))
	}
	if len(req.Schema) > 0 {
		var schema map[string]any
		if err := json.Unmarshal(req.Schema, &schema); err != nil || schema == nil {
			return false, newError(InvalidConfig, "validate schema", err)
		}
	} else if strings.TrimSpace(req.SchemaName) != "" {
		return false, newError(InvalidConfig, "validate schema", fmt.Errorf("schema name without schema"))
	}
	webSearch := false
	seen := map[Tool]bool{}
	for _, tool := range req.Tools {
		if seen[tool] {
			return false, newError(InvalidConfig, "validate tools", fmt.Errorf("duplicate tool"))
		}
		seen[tool] = true
		switch tool {
		case ToolWebSearch:
			webSearch = true
		default:
			return false, newError(UnsupportedCapability, "validate tools", fmt.Errorf("unsupported tool"))
		}
	}
	if req.MaxWebSearchCalls > 0 && !webSearch {
		return false, newError(InvalidConfig, "validate tools", fmt.Errorf("web search limit without tool"))
	}
	return webSearch, nil
}

func execArgs(req Request, webSearch bool) []string {
	reasoning := strings.TrimSpace(req.ReasoningEffort)
	if reasoning == "" || reasoning == "none" {
		reasoning = "low"
	}
	args := []string{"exec", "--strict-config", "--json", "--ephemeral", "--ignore-user-config"}
	if model := strings.TrimSpace(req.Model); model != "" {
		args = append(args, "--model", model)
	}
	args = append(args,
		"--config", "approval_policy=\"never\"",
		"--config", "sandbox_mode=\"read-only\"",
		"--config", "suppress_unstable_features_warning=true",
		"--config", fmt.Sprintf("tools.web_search=%t", webSearch),
		"--config", fmt.Sprintf("model_reasoning_effort=%q", reasoning),
		"--config", fmt.Sprintf("features.code_mode=%t", webSearch),
		"--config", fmt.Sprintf("features.code_mode_host=%t", webSearch),
		"--config", "features.shell_tool=false", "--config", "features.unified_exec=false",
		"--config", "features.apps=false", "--config", "features.hooks=false",
		"--config", "features.browser_use=false", "--config", "features.computer_use=false",
		"--config", "features.image_generation=false", "--config", "features.plugins=false",
		"--config", "features.multi_agent=false", "--config", "features.multi_agent_v2=false",
		"--config", "features.skill_search=false", "--config", "features.view_image=false",
		"--config", "forced_login_method=\"chatgpt\"",
		"--config", "shell_environment_policy.inherit=\"none\"",
	)
	if webSearch {
		args = append(args, "--config", "web_search=\"live\"")
	}
	return args
}

func applyProtocolResult(result *Result, parsed ProtocolResult) {
	result.Output = parsed.Raw
	result.Events = parsed.Events
	result.Tools.WebSearchCalls = parsed.OfficialWebSearchCalls
	result.Tools.InferredSearchQueries = parsed.InferredSearchQueries
	if parsed.Model != "" {
		result.Model = parsed.Model
	}
	if parsed.ThreadID != "" {
		result.ThreadID = parsed.ThreadID
	}
	result.Usage = Usage{InputTokens: parsed.InputTokens, OutputTokens: parsed.OutputTokens, CachedInputTokens: parsed.CachedInputTokens, ReasoningOutputTokens: parsed.ReasoningOutputTokens, Reported: parsed.TokensReported}
}

type limitedRead struct {
	data     []byte
	tooLarge bool
	err      error
}

func readLimited(reader io.Reader, limit int64, out chan<- limitedRead) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	tooLarge := int64(len(data)) > limit
	if tooLarge {
		data = data[:limit]
		if _, drainErr := io.Copy(io.Discard, reader); err == nil {
			err = drainErr
		}
	}
	out <- limitedRead{data: data, tooLarge: tooLarge, err: err}
}

func awaitPipe(ch <-chan limitedRead, pipe io.Closer) limitedRead {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case result := <-ch:
		return result
	case <-timer.C:
		_ = pipe.Close()
		return limitedRead{err: fmt.Errorf("output pipe did not close")}
	}
}

func waitProcess(process *os.Process) error {
	state, err := process.Wait()
	if err != nil {
		return err
	}
	if state.Success() {
		return nil
	}
	return &exec.ExitError{ProcessState: state}
}

func terminateProcessGroup(pid int) bool {
	if pid <= 0 {
		return false
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	if waitProcessGroupGone(pid, 250*time.Millisecond) {
		return true
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	return waitProcessGroupGone(pid, time.Second)
}

func waitProcessGroupGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if err := syscall.Kill(-pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func classifyStartError(err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return newError(InvalidConfig, "start", err)
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && (errors.Is(pathErr.Err, os.ErrNotExist) || errors.Is(pathErr.Err, os.ErrPermission) || errors.Is(pathErr.Err, syscall.ENOEXEC)) {
		return newError(InvalidConfig, "start", err)
	}
	return contextOr(StartFailed, "start", err)
}

func classifyProcessError(err error, stderr []byte) error {
	signal := strings.ToLower(string(stderr))
	if containsAny(signal, "auth expired", "authentication expired", "login required", "not logged in", "chatgpt authentication", "authentication failed", "unauthorized", "http 401", "status 401") {
		return newError(AuthenticationFailed, "execute", err)
	}
	if containsAny(signal, "rate limit", "rate_limit", "rate-limited", "too many requests", "http 429", "status 429") {
		return newError(RateLimited, "execute", err)
	}
	return contextOr(ProcessFailed, "execute", err)
}

func contextError(op string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return newError(TimedOut, op, err)
	}
	if errors.Is(err, context.Canceled) {
		return newError(Cancelled, op, err)
	}
	return newError(Unknown, op, err)
}

func contextOr(fallback ErrorKind, op string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return contextError(op, err)
	}
	return newError(fallback, op, err)
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
func firstError(values ...error) error {
	for _, err := range values {
		if err != nil {
			return err
		}
	}
	return nil
}
func debugPayload(data []byte) (string, bool) {
	if len(data) <= debugPayloadMaxBytes {
		return string(data), false
	}
	return string(data[:debugPayloadMaxBytes]), true
}

func newAttemptID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "codex-" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("codex-%d", time.Now().UnixNano())
}

var _ Runner = (*Executor)(nil)
