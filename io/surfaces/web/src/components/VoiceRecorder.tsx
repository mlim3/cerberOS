import { useState, useRef, useCallback, useEffect } from 'react'
import { AudioCapture, type CaptureResult } from '../voice/AudioCapture'

interface VoiceRecorderProps {
  onTranscription: (text: string) => void
  disabled?: boolean
  /** Called with the raw capture result (for sending to IO API) */
  onAudioCapture?: (result: CaptureResult) => void
}

export function VoiceRecorder({ onTranscription, disabled, onAudioCapture }: VoiceRecorderProps) {
  const [recording, setRecording] = useState(false)
  const [transcribing, setTranscribing] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const captureRef = useRef<AudioCapture | null>(null)
  const [voiceEnabled, setVoiceEnabled] = useState<boolean | null>(null)

  useEffect(() => {
    fetch('/api/status')
      .then(r => r.json())
      .then((data: { voice_enabled?: boolean }) => setVoiceEnabled(data.voice_enabled ?? true))
      .catch(() => setVoiceEnabled(true))
  }, [])

  const handleClick = useCallback(async () => {
    if (recording) {
      // Stop recording
      try {
        setTranscribing(true)
        setError(null)
        const result = await captureRef.current?.stop()
        setRecording(false)

        if (!result) return

        onAudioCapture?.(result)

        // Send to IO API for transcription
        const res = await fetch('/api/voice/transcribe', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            audioData: result.audioData,
            format: result.format,
          }),
        })

        if (!res.ok) {
          throw new Error(`Transcription failed: ${res.status}`)
        }

        const data = await res.json() as { text: string; disabled?: boolean }
        if (data.disabled) {
          setError('Voice transcription is disabled in this deployment.')
          return
        }
        if (data.text.trim()) {
          onTranscription(data.text.trim())
        }
      } catch (err) {
        setError(String(err))
      } finally {
        setTranscribing(false)
      }
    } else {
      // Start recording
      try {
        setError(null)
        captureRef.current = new AudioCapture()
        await captureRef.current.start()
        setRecording(true)
      } catch (err) {
        setError(String(err))
      }
    }
  }, [recording, onTranscription, onAudioCapture])

  if (voiceEnabled === false || !AudioCapture.isSupported()) {
    return null
  }

  return (
    <div className="voice-recorder">
      <button
        className={`voice-btn ${recording ? 'recording' : ''} ${transcribing ? 'transcribing' : ''}`}
        onClick={handleClick}
        disabled={disabled || transcribing}
        aria-label={recording ? 'Stop recording' : 'Start voice input'}
        title={recording ? 'Click to stop' : 'Click to record'}
      >
        {transcribing ? (
          <span className="voice-spinner">⟳</span>
        ) : recording ? (
          <span className="voice-stop">■</span>
        ) : (
          <span className="voice-mic">🎤</span>
        )}
      </button>
      {error && <span className="voice-error">{error}</span>}
    </div>
  )
}
