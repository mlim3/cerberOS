/**
 * SSE client for CLI. Uses fetch + ReadableStream to consume Server-Sent Events.
 * Compatible with the IO API's /api/events/:taskId endpoint.
 */

import type { StatusUpdate, CredentialRequest, OrchestratorStreamEvent } from '@cerberos/io-core'
import { parseOrchestratorStreamEvent } from '@cerberos/io-core'

export interface SSEClientOptions {
  url: string
  onStatusUpdate?: (update: StatusUpdate) => void
  onCredentialRequest?: (request: CredentialRequest) => void
  onError?: (err: Error) => void
  signal?: AbortSignal
}

export class SSEClient {
  private url: string
  private onStatusUpdate?: (u: StatusUpdate) => void
  private onCredentialRequest?: (r: CredentialRequest) => void
  private onError?: (e: Error) => void
  private signal?: AbortSignal
  private abortController: AbortController

  constructor(options: SSEClientOptions) {
    this.url = options.url
    this.onStatusUpdate = options.onStatusUpdate
    this.onCredentialRequest = options.onCredentialRequest
    this.onError = options.onError
    this.signal = options.signal
    this.abortController = new AbortController()
  }

  async connect(): Promise<void> {
    try {
      const res = await fetch(this.url, {
        signal: this.abortController.signal,
        headers: {
          'Accept': 'text/event-stream',
          'Cache-Control': 'no-cache',
        },
      })

      if (!res.ok) {
        throw new Error(`SSE connection failed: ${res.status} ${res.statusText}`)
      }

      if (!res.body) {
        throw new Error('SSE response has no body')
      }

      const reader = res.body.getReader()
      const decoder = new TextDecoder()
      let buffer = ''

      try {
        while (true) {
          const { done, value } = await reader.read()
          if (done) break

          buffer += decoder.decode(value, { stream: true })
          const lines = buffer.split('\n')
          buffer = lines.pop() ?? ''

          for (const line of lines) {
            if (!line.startsWith('data: ')) continue
            const data = line.slice(6).trim()
            if (!data) continue

            try {
              const raw = JSON.parse(data) as unknown
              const event = parseOrchestratorStreamEvent(raw)
              if (!event) continue

              if (event.type === 'status') {
                this.onStatusUpdate?.(event.payload)
              } else if (event.type === 'credential_request') {
                this.onCredentialRequest?.(event.payload)
              }
            } catch {
              // Ignore parse errors for individual frames
            }
          }
        }
      } finally {
        reader.releaseLock()
      }
    } catch (err) {
      if ((err as Error).name === 'AbortError') {
        return // Intentional disconnect
      }
      this.onError?.(err as Error)
    }
  }

  disconnect(): void {
    this.abortController.abort()
  }
}

/**
 * Subscribe to orchestrator events for a specific task.
 * Returns a cleanup function.
 */
export function subscribeToTask(
  taskId: string,
  apiBase: string,
  handlers: {
    onStatusUpdate?: (u: StatusUpdate) => void
    onCredentialRequest?: (r: CredentialRequest) => void
    onError?: (e: Error) => void
  },
): () => void {
  const url = `${apiBase}/api/events/${encodeURIComponent(taskId)}`
  const client = new SSEClient({ url, ...handlers })

  // Connect in background
  client.connect().catch(handlers.onError)

  return () => {
    client.disconnect()
  }
}
