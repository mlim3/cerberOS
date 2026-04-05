/**
 * Memory service client for CLI surface.
 * Handles credential storage through the secure channel.
 */

const DEFAULT_MEMORY_API_BASE = process.env.MEMORY_API_BASE ?? 'http://localhost:8080'

export interface CredentialSubmission {
  taskId: string
  requestId: string
  userId: string
  keyName: string
  value: string
}

export interface CredentialSubmitResult {
  ok: boolean
  keyName?: string
  error?: string
}

/**
 * Submit a credential to Memory Vault through the IO API's secure channel.
 * This never touches the orchestrator or the chat pipeline.
 */
export async function submitCredential(
  apiBase: string,
  params: CredentialSubmission
): Promise<CredentialSubmitResult> {
  try {
    const res = await fetch(`${apiBase}/api/credential`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Surface-Key': 'cli',
      },
      body: JSON.stringify(params),
    })

    if (!res.ok) {
      const err = await res.text()
      return { ok: false, error: `HTTP ${res.status}: ${err}` }
    }

    return { ok: true, keyName: params.keyName }
  } catch (err) {
    return { ok: false, error: String(err) }
  }
}
