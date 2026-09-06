// Package codexcli executes the authenticated Codex CLI as a bounded,
// non-interactive provider. It intentionally contains no application-domain
// concepts, persistence, retries, fallback, or budget policy.
package codexcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const (
	DefaultTimeout          = 5 * time.Minute
	DefaultJSONLMaxBytes    = int64(16 << 20)
	DefaultResultMaxBytes   = int64(1 << 20)
	DefaultStderrMaxBytes   = int64(1 << 20)
	DefaultMetadataMaxBytes = 512
)

// Config controls the local process boundary. CodexHome must be an explicit,
// externally provisioned directory containing Codex authentication.
type Config struct {
	BinaryPath       string
	CodexHome        string
	ScratchParent    string
	Timeout          time.Duration
	JSONLMaxBytes    int64
	ResultMaxBytes   int64
	StderrMaxBytes   int64
	MetadataMaxBytes int
	Limiter          *Limiter
	Logger           *slog.Logger
	// LogName customizes the stable technical event prefix for an embedding
	// application. Empty defaults to "codex cli".
	LogName string
	// DebugPayloads logs bounded stdout/stderr at DEBUG level. It is unsafe for
	// production because provider output can contain sensitive content.
	DebugPayloads bool
}

type Tool string

const ToolWebSearch Tool = "web_search"

// Request describes one independent Codex invocation. Prompt is passed only
// over stdin. Schema must contain a valid JSON Schema object when present.
type Request struct {
	Prompt            string          `json:"prompt"`
	Model             string          `json:"model,omitempty"`
	ReasoningEffort   string          `json:"reasoning_effort,omitempty"`
	SchemaName        string          `json:"schema_name,omitempty"`
	Schema            json.RawMessage `json:"schema,omitempty"`
	Tools             []Tool          `json:"tools,omitempty"`
	MaxWebSearchCalls int             `json:"max_web_search_calls,omitempty"`
}

type Usage struct {
	InputTokens           int  `json:"input_tokens"`
	OutputTokens          int  `json:"output_tokens"`
	CachedInputTokens     *int `json:"cached_input_tokens,omitempty"`
	ReasoningOutputTokens *int `json:"reasoning_output_tokens,omitempty"`
	Reported              bool `json:"reported"`
}

type ToolUsage struct {
	WebSearchCalls        int `json:"web_search_calls"`
	InferredSearchQueries int `json:"inferred_search_queries"`
}

// Result is returned even when Run fails. Consumers should persist any usage,
// identifiers, or tool evidence already reported before applying retry/fallback
// policy of their own.
type Result struct {
	Output          string        `json:"output,omitempty"`
	Model           string        `json:"model,omitempty"`
	ThreadID        string        `json:"thread_id,omitempty"`
	AttemptID       string        `json:"attempt_id"`
	Usage           Usage         `json:"usage"`
	Tools           ToolUsage     `json:"tools"`
	Duration        time.Duration `json:"-"`
	QueueDuration   time.Duration `json:"-"`
	DurationMS      int64         `json:"duration_ms"`
	QueueMS         int64         `json:"queue_ms"`
	Events          int           `json:"events"`
	OrphanProcesses int           `json:"orphan_processes"`
	StdoutTruncated bool          `json:"stdout_truncated"`
	StderrTruncated bool          `json:"stderr_truncated"`
}

type ErrorKind string

const (
	InvalidConfig         ErrorKind = "invalid_config"
	UnsupportedCapability ErrorKind = "unsupported_capability"
	StartFailed           ErrorKind = "start_failed"
	Cancelled             ErrorKind = "cancelled"
	TimedOut              ErrorKind = "timeout"
	ProcessFailed         ErrorKind = "process_failed"
	ProtocolInvalid       ErrorKind = "protocol_invalid"
	SchemaInvalid         ErrorKind = "schema_invalid"
	AuthenticationFailed  ErrorKind = "authentication_failed"
	RateLimited           ErrorKind = "rate_limited"
	Unknown               ErrorKind = "unknown"
)

// Error exposes a stable, payload-safe category. Error never includes stderr,
// prompt text, credentials, paths, or raw protocol output.
type Error struct {
	Kind  ErrorKind `json:"kind"`
	Op    string    `json:"op,omitempty"`
	Cause error     `json:"-"`
}

func (e *Error) Error() string {
	if e == nil || e.Kind == "" {
		return "codex cli error"
	}
	if e.Op == "" {
		return "codex cli " + string(e.Kind)
	}
	// Web-search contract failures are library-authored text and contain no
	// provider payload. Keeping this detail makes capability errors actionable.
	if e.Op == "validate web search" && e.Cause != nil {
		return fmt.Sprintf("codex cli %s: %s: %v", e.Op, e.Kind, e.Cause)
	}
	return fmt.Sprintf("codex cli %s: %s", e.Op, e.Kind)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && e != nil && other != nil && e.Kind == other.Kind
}

func KindOf(err error) ErrorKind {
	if err == nil {
		return ""
	}
	var target *Error
	if errors.As(err, &target) && target != nil {
		return target.Kind
	}
	return Unknown
}

func newError(kind ErrorKind, op string, cause error) error {
	if kind == "" {
		kind = Unknown
	}
	return &Error{Kind: kind, Op: op, Cause: cause}
}

// Runner is the minimal seam applications should depend on.
type Runner interface {
	Run(context.Context, Request) (Result, error)
	IsConfigured() bool
}
