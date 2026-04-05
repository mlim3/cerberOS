/**
 * Browser-side audio capture using MediaRecorder API.
 * Captures microphone input and returns base64-encoded audio.
 */

export interface AudioCaptureConfig {
  format?: 'webm' | 'wav'
  mimeType?: string
  maxDurationMs?: number
}

export interface CaptureResult {
  audioData: string  // base64-encoded audio
  format: string
  durationMs: number
  mimeType: string
}

const DEFAULT_MIME_TYPE = 'audio/webm;codecs=opus'

export class AudioCapture {
  private mediaRecorder: MediaRecorder | null = null
  private audioChunks: Blob[] = []
  private startTime = 0
  private config: Required<AudioCaptureConfig>

  constructor(config: AudioCaptureConfig = {}) {
    this.config = {
      format: config.format ?? 'webm',
      mimeType: config.mimeType ?? DEFAULT_MIME_TYPE,
      maxDurationMs: config.maxDurationMs ?? 60000,
    }
  }

  /** Check if audio capture is supported */
  static isSupported(): boolean {
    return typeof MediaRecorder !== 'undefined' && MediaRecorder.isTypeSupported?.('audio/webm;codecs=opus')
  }

  /** Start recording */
  async start(): Promise<void> {
    const stream = await navigator.mediaDevices.getUserMedia({ audio: true })
    this.audioChunks = []
    this.startTime = Date.now()

    // Fallback mime type if preferred is not supported
    let mimeType = this.config.mimeType
    if (!MediaRecorder.isTypeSupported(mimeType)) {
      const alternatives = ['audio/webm', 'audio/ogg', 'audio/mp4']
      mimeType = alternatives.find(t => MediaRecorder.isTypeSupported(t)) ?? 'audio/webm'
    }

    this.mediaRecorder = new MediaRecorder(stream, { mimeType })

    this.mediaRecorder.ondataavailable = (e) => {
      if (e.data.size > 0) {
        this.audioChunks.push(e.data)
      }
    }

    this.mediaRecorder.onerror = () => {
      stream.getTracks().forEach(t => t.stop())
    }

    this.mediaRecorder.start()

    // Auto-stop after max duration
    setTimeout(() => {
      if (this.mediaRecorder?.state === 'recording') {
        this.mediaRecorder.stop()
      }
    }, this.config.maxDurationMs)
  }

  /** Stop recording and return base64 audio */
  async stop(): Promise<CaptureResult> {
    if (!this.mediaRecorder || this.mediaRecorder.state === 'inactive') {
      throw new Error('Not recording')
    }

    const recorder = this.mediaRecorder

    // Wait for onstop to fire (which collects final chunks) before proceeding
    await new Promise<void>(resolve => {
      recorder.onstop = () => {
        // Stop all tracks on the stream
        recorder.stream.getTracks().forEach(t => t.stop())
        resolve()
      }
      recorder.stop()
    })

    this.mediaRecorder = null

    const blob = new Blob(this.audioChunks, { type: this.audioChunks[0]?.type ?? this.config.mimeType })
    const arrayBuffer = await blob.arrayBuffer()
    const base64 = btoa(
      String.fromCharCode(...new Uint8Array(arrayBuffer))
    )

    return {
      audioData: `data:${blob.type};base64,${base64}`,
      format: this.config.format,
      durationMs: Date.now() - this.startTime,
      mimeType: blob.type,
    }
  }

  /** Cancel recording without producing output */
  cancel(): void {
    if (this.mediaRecorder?.state === 'recording') {
      this.mediaRecorder.stop()
    }
    this.audioChunks = []
  }

  /** Get current recording state */
  isRecording(): boolean {
    return this.mediaRecorder?.state === 'recording'
  }
}
