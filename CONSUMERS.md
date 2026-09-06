# Consumer contract audit

This audit records the evidence used to keep the provider API independent. It
is descriptive of the initial GOATI consumer on 2026-09-06; application code is
still authoritative.

## GOATI

Concrete flow: a persisted `llm_profile` selects `chatgpt_subscription`; the
`internal/llm.Service` constructs a matcher transport; matching, discovery,
recategorization, validation, and spec generation supply their own prompt and
schema through `matcher.ProviderCompletionRequest`.

| GOATI field or behavior | Provider contract | Ownership |
|---|---|---|
| Prompt, selected model, reasoning effort | `Request` | GOATI builds/selects; library transports |
| Strict output schema and schema name | `Request.Schema`, `Request.SchemaName` | GOATI owns domain schema |
| Hosted Web Search request | `Request.Tools` | GOATI selects; library enables and validates protocol evidence |
| Final JSON text | `Result.Output` | GOATI performs domain decoding/validation |
| Model, request/thread identifier, token counts | `Result.Model`, `AttemptID`, `ThreadID`, `Usage` | Library reports; GOATI persists |
| Tool counts | `Result.Tools` | Library reports; GOATI applies fallback safeguards |
| Capacity-one process serialization | Shared `Limiter` in GOATI adapter | GOATI policy |
| Governance reservation and durable attempt rows | No library type | GOATI |
| Retry, paid-provider fallback, cache, budgets | No library behavior | GOATI |

The profile adapter is `backend/internal/llm/codex_transport.go`. The separately
pinned merchant-search source also executes and parses through this module while
retaining its application-owned prompt, domain mapper, governor, durable usage
records, fallback taxonomy, and live preflight. GOATI's old process and JSONL
parser implementations have been removed.

## Other consumers

No second application is required to complete or version the initial library.
Go consumers can import the module directly. Other runtimes can use the optional
one-shot JSON executable demonstrated by `examples/bun-client.ts`. In either
case, prompts, domain schemas, persistence, retry, fallback, budgets, and data
handling policy remain outside the library.

## Release boundary

The library is published from `github.com/goati-app/codex-cli-provider` and its
current release is tagged `v0.1.1`. GOATI consumes that version without a local
`replace`, so its standalone and Docker builds do not depend on a sibling
checkout.
