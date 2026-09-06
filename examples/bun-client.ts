// Minimal process adapter for a future Bun/TypeScript consumer. Domain prompts,
// validation, persistence, retries, and budgets intentionally stay outside it.
export type CodexRequest = {
  prompt: string
  model?: string
  reasoning_effort?: string
  schema_name?: string
  schema?: Record<string, unknown>
  tools?: Array<'web_search'>
  max_web_search_calls?: number
}

export type CodexResponse = {
  result: {
    output?: string
    model?: string
    thread_id?: string
    attempt_id: string
    usage: {
      input_tokens: number
      output_tokens: number
      cached_input_tokens?: number
      reasoning_output_tokens?: number
      reported: boolean
    }
    tools: { web_search_calls: number; inferred_search_queries: number }
    duration_ms: number
    queue_ms: number
  }
  error?: { kind: string; message: string }
}

export async function runCodex(
  executable: string,
  codexHome: string,
  request: CodexRequest,
  signal?: AbortSignal
): Promise<CodexResponse> {
  const child = Bun.spawn([executable, '--codex-home', codexHome], {
    stdin: new Blob([JSON.stringify(request)]),
    stdout: 'pipe',
    stderr: 'pipe',
    signal,
  })
  const payload = (await new Response(child.stdout).json()) as CodexResponse
  await child.exited
  return payload
}
