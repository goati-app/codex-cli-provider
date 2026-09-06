// codex-cli-provider is a one-shot JSON bridge for non-Go consumers. It is not
// a daemon or network service: one stdin request causes one Codex attempt and
// one stdout response.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	codexcli "github.com/goati-app/codex-cli-provider"
)

type response struct {
	Result codexcli.Result `json:"result"`
	Error  *errorResponse  `json:"error,omitempty"`
}

type errorResponse struct {
	Kind    codexcli.ErrorKind `json:"kind"`
	Message string             `json:"message"`
}

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout)) }

func run(args []string, stdin io.Reader, stdout io.Writer) int {
	var cfg codexcli.Config
	var timeout time.Duration
	flags := flag.NewFlagSet("codex-cli-provider", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&cfg.BinaryPath, "codex-binary", "codex", "path to the Codex executable")
	flags.StringVar(&cfg.CodexHome, "codex-home", "", "explicit authenticated CODEX_HOME (required)")
	flags.StringVar(&cfg.ScratchParent, "scratch-parent", "", "parent directory for private temporary files")
	flags.DurationVar(&timeout, "timeout", codexcli.DefaultTimeout, "maximum time including local capacity wait")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	cfg.Timeout = timeout
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))

	encoder := json.NewEncoder(stdout)
	fail := func(result codexcli.Result, err error) {
		_ = encoder.Encode(response{Result: result, Error: &errorResponse{Kind: codexcli.KindOf(err), Message: err.Error()}})
	}
	executor, err := codexcli.New(cfg)
	if err != nil {
		fail(codexcli.Result{}, err)
		return 1
	}
	var req codexcli.Request
	decoder := json.NewDecoder(io.LimitReader(stdin, codexcli.DefaultJSONLMaxBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		fail(codexcli.Result{}, fmt.Errorf("invalid request JSON"))
		return 1
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		fail(codexcli.Result{}, fmt.Errorf("invalid request JSON"))
		return 1
	}
	result, err := executor.Run(context.Background(), req)
	if err != nil {
		fail(result, err)
		return 1
	}
	if err := encoder.Encode(response{Result: result}); err != nil {
		return 1
	}
	return 0
}
