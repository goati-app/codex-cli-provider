# codex-cli-provider

`codex-cli-provider` is a small Go library for running one authenticated Codex
CLI attempt and interpreting its JSONL protocol. It is shared infrastructure,
not an LLM gateway: prompts, domain validation, persistence, retry, fallback,
budgets, and cross-process coordination remain in the consuming application.
The `github.com/goati-app/...` module path identifies its repository owner; the
package API and implementation contain no GOATI business types or behavior.

The initial implementation supports Linux and other Unix systems. It relies on
Unix process groups to terminate descendants and deliberately returns
`unsupported_capability` on non-Unix platforms instead of claiming incomplete
Windows support.

## Requirements and compatibility

- Go 1.25 or newer.
- An explicit, writable `CODEX_HOME` provisioned outside the library. The
  library neither logs in nor copies credentials.
- A Codex CLI whose `exec` command supports `--json`, `--ephemeral`,
  `--ignore-user-config`, `--output-schema`,
  `--skip-git-repo-check`, and stdin via `-`.
- Verified locally with `codex-cli 0.153.4`. GOATI's merchant-search consumer
  retains its separate `0.149.0` preflight pin until that application explicitly
  advances and verifies its live deployment image.

Install the tagged module with:

```sh
go get github.com/goati-app/codex-cli-provider@v0.1.1
```

The command is invoked without a shell. The child receives only `CODEX_HOME`, a
private `HOME`, a private `TMPDIR`, a closed `PATH`, and UTF-8 locale variables.
User config is ignored while exec-policy rules remain active as an additional
restriction layer. Approvals are disabled; the sandbox is read-only; local shell, file, browser, app, plugin, skill, image,
computer, and multi-agent capabilities are disabled. Web Search is available
only when requested explicitly.

## Go usage

```go
limiter := codexcli.NewLimiter(1) // optional; share to coordinate instances
executor, err := codexcli.New(codexcli.Config{
    BinaryPath:    "codex",
    CodexHome:     "/var/lib/my-app/codex",
    ScratchParent: "/var/lib/my-app/tmp",
    Timeout:       90 * time.Second,
    Limiter:       limiter,
})
if err != nil {
    return err
}

schema := json.RawMessage(`{
  "type":"object",
  "additionalProperties":false,
  "properties":{"ok":{"type":"boolean"}},
  "required":["ok"]
}`)
result, runErr := executor.Run(ctx, codexcli.Request{
    Prompt:          promptBuiltByTheApplication,
    Model:           "gpt-5.6-luna",
    ReasoningEffort: "low",
    SchemaName:      "example_result",
    Schema:          schema,
})
// Always record result before applying application retry/fallback policy:
recordAttempt(result.AttemptID, result.Usage, result.Duration, runErr)
if runErr != nil {
    return runErr
}
useDomainOutput(result.Output)
```

For Web Search, add `Tools: []codexcli.Tool{codexcli.ToolWebSearch}`. The result
reports official search actions separately from inferred query count. The
default postcondition permits at most three official searches and requires the
final agent message to occur after the last search.

## Cancellation, errors, and partial results

`Run` includes cancelable limiter wait and process setup in its timeout. On
cancellation or timeout it sends `SIGTERM`, then `SIGKILL` after a bounded grace
period, to the complete process group. Pipes are drained with retention limits
so descendants cannot block the parent indefinitely.

Use `codexcli.KindOf(err)` instead of matching text. Stable kinds are:

- `invalid_config` and `unsupported_capability`
- `start_failed`, `cancelled`, `timeout`, and `process_failed`
- `protocol_invalid` and `schema_invalid`
- `authentication_failed`, `rate_limited`, and `unknown`

Authentication and rate-limit kinds are assigned only when Codex supplies a
recognized signal. Error strings never contain stderr, raw JSONL, prompt text,
credentials, or filesystem paths.

`Result` is meaningful when `err != nil`. In particular, it retains final output
when available, usage, thread/model identifiers, and hosted-tool evidence seen
before a malformed event, timeout, or failed exit. `Usage.Reported` distinguishes
an absent token report from a reported value of zero. The library never converts
ChatGPT subscription usage into an assumed API cost.

Cancellation is ordinary context cancellation; no library-specific handle is
required:

```go
ctx, cancel := context.WithCancel(parent)
go func() {
    <-shutdown
    cancel()
}()
result, err := executor.Run(ctx, request)
```

## Non-Go consumers

Applications that cannot import the Go package can use the optional one-shot
process bridge without introducing a daemon or network service:

```sh
go build -buildvcs=false -o ./bin/codex-cli-provider ./cmd/codex-cli-provider
printf '%s\n' '{"prompt":"Return ok","model":"gpt-5.6-luna"}' \
  | ./bin/codex-cli-provider --codex-home /explicit/codex/home
```

It reads exactly one JSON request and writes exactly one JSON response. A failed
attempt exits non-zero but still writes the partial result and typed error. It
does not listen on a socket, persist data, retry, or choose fallback providers.
Each application remains responsible for defining its prompt, domain schema,
persistence relation, and retry/budget policy.

## Logging and security

Technical logging is optional and excludes prompts, paths, stderr, and raw
output by default. `DebugPayloads` is an explicit local-only diagnostic escape
hatch; enabling it can log sensitive model output. Scratch files use private
permissions and are removed after every return path.

## Verification

Routine tests use fake executables and JSONL fixtures; they require no Codex
credentials or paid request:

```sh
go test ./...
```

Live tests belong in consumers because they must declare the supported Codex
version, model, capabilities, and account policy explicitly.

## Release packaging

Tagged releases are published as a normal Go module from
`github.com/goati-app/codex-cli-provider`. Consumers should depend on an explicit
version rather than a sibling-directory `replace`; this keeps standalone and
Docker builds independent of the checkout layout.
