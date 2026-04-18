/**
 * Manages the Python faster-whisper subprocess.
 * Wraps the CLI interface with a clean async API.
 * Falls back to a cloud STT API (OpenAI Whisper) on local failure.
 */

import { spawn } from 'child_process'
import { writeFileSync, unlinkSync, mkdtempSync } from 'fs'
import { randomUUID } from 'crypto'
import { ioLog } from '../logger'

const VENV_PYTHON = '/opt/venv/bin/python3'
const SCRIPT_PATH = new URL('./cli.py', import.meta.url).pathname

// Cloud fallback config
const STT_PROVIDER = process.env.STT_PROVIDER ?? 'local'
const OPENAI_API_KEY = process.env.OPENAI_API_KEY ?? ''
const STT_API_URL =
  process.env.STT_API_URL ??
  process.env.STt_API_URL ??
  'https://api.openai.com/v1/audio/transcriptions'
const STT_MODEL = process.env.STT_MODEL ?? 'whisper-1'

function isSttDisabled(): boolean {
  const p = STT_PROVIDER.toLowerCase()
  return p === 'disabled' || p === 'off' || p === 'none'
}

let proc: ReturnType<typeof spawn> | null = null
let ready = false
let currentResolve: ((v: boolean) => void) | null = null
let pendingRequest: {
  resolve: (v: TranscriptionResult) => void
  reject: (e: Error) => void
} | null = null

function getProc(): ReturnType<typeof spawn> {
  if (!proc) {
    proc = spawn(VENV_PYTHON, [SCRIPT_PATH], {
      stdio: ['pipe', 'pipe', 'pipe'],
    })

    proc.stderr?.on('data', (d) => {
      ioLog('error', 'transcription', 'Python stderr', { line: d.toString().trim() })
    })

    proc.on('close', (code) => {
      ioLog('warn', 'transcription', 'Python process exited', { code })
      proc = null
      ready = false
      // Reject any pending request
      pendingRequest?.reject(new Error(`Transcription process exited: ${code}`))
      pendingRequest = null
    })

    // Signal that the process is ready
    proc.stdout?.on('data', (d) => {
      const line = d.toString().trim()
      if (line === 'READY') {
        ready = true
        currentResolve?.(true)
        currentResolve = null
      } else if (pendingRequest && line.length > 0) {
        // Got a response
        try {
          const result = JSON.parse(line)
          if (result.error) {
            pendingRequest.reject(new Error(result.error))
          } else {
            pendingRequest.resolve(result as TranscriptionResult)
          }
        } catch {
          pendingRequest.reject(new Error(`Invalid JSON from Python: ${line}`))
        }
        pendingRequest = null
      }
    })
  }
  return proc
}

async function ensureReady(): Promise<void> {
  if (ready) return
  return new Promise((resolve) => {
    currentResolve = resolve
    getProc()
  })
}

export interface TranscriptionResult {
  text: string
  language: string
  duration: number
  /** Present when STT_PROVIDER is disabled — UI can show a friendly message */
  disabled?: boolean
}

export interface TranscriptionOptions {
  audioData: string // base64 audio
  language?: string
}

/**
 * Cloud STT fallback using OpenAI Whisper API.
 */
async function transcribeViaCloud(options: TranscriptionOptions): Promise<TranscriptionResult> {
  if (!OPENAI_API_KEY) {
    throw new Error('Cloud transcription requested but OPENAI_API_KEY is not set')
  }

  // Decode base64 audio and write to temp file
  let audioData = options.audioData
  if (audioData.startsWith('data:')) {
    audioData = audioData.split(',', 1)[1]
  }
  const audioBytes = Buffer.from(audioData, 'base64')

  const tmpDir = mkdtempSync('/tmp/cerberOS-voice-')
  const tmpPath = `${tmpDir}/audio.webm`
  writeFileSync(tmpPath, audioBytes)

  try {
    const formData = new FormData()
    formData.append('file', new Blob([audioBytes]), 'audio.webm')
    formData.append('model', STT_MODEL)
    if (options.language) {
      formData.append('language', options.language)
    }

    const res = await fetch(STT_API_URL, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${OPENAI_API_KEY}`,
      },
      body: formData,
    })

    if (!res.ok) {
      const errText = await res.text()
      throw new Error(`Cloud STT API returned ${res.status}: ${errText}`)
    }

    const data = (await res.json()) as { text?: string; language?: string }
    return {
      text: (data.text ?? '').trim(),
      language: data.language ?? options.language ?? 'en',
      duration: 0, // cloud API doesn't return duration
    }
  } finally {
    unlinkSync(tmpPath)
  }
}

/**
 * Send a transcription request — tries local faster-whisper first,
 * falls back to cloud STT API on failure.
 */
export async function transcribe(options: TranscriptionOptions): Promise<TranscriptionResult> {
  if (isSttDisabled()) {
    return {
      text: '',
      language: options.language ?? 'en',
      duration: 0,
      disabled: true,
    }
  }

  // Cloud-only mode: skip local
  if (STT_PROVIDER === 'cloud') {
    return transcribeViaCloud(options)
  }

  // Try local first
  try {
    await ensureReady()
    const proc = getProc()

    const result = await new Promise<TranscriptionResult>((resolve, reject) => {
      pendingRequest = { resolve, reject }

      proc.on('close', (code) => {
        if (pendingRequest) {
          reject(new Error(`Transcription process exited: ${code}`))
          pendingRequest = null
        }
      })

      const request = JSON.stringify({
        audioData: options.audioData,
        language: options.language,
      })
      proc.stdin?.write(request + '\n')
    })

    return result
  } catch (localErr) {
    // Local failed — try cloud fallback
    if (STT_PROVIDER === 'local' && OPENAI_API_KEY) {
      ioLog('warn', 'transcription', 'local STT failed, trying cloud fallback', { error: String(localErr) })
      return transcribeViaCloud(options)
    }
    // No fallback available, surface the original error
    throw localErr
  }
}

/**
 * Warm up the transcription model on startup.
 * Call this once when the io-api server starts.
 */
export async function warmupTranscription(): Promise<void> {
  if (isSttDisabled()) {
    ioLog('info', 'transcription', 'STT disabled, skipping model warmup')
    return
  }

  if (STT_PROVIDER === 'cloud') {
    ioLog('info', 'transcription', 'cloud-only mode, skipping model warmup')
    return
  }

  ioLog('info', 'transcription', 'warming up faster-whisper model')
  try {
    await ensureReady()
    ioLog('info', 'transcription', 'model ready')
  } catch (err) {
    ioLog('warn', 'transcription', 'warmup failed, will retry on first request', { error: String(err) })
    if (OPENAI_API_KEY) {
      ioLog('info', 'transcription', 'cloud fallback configured, will be used on first request')
    }
  }
}
