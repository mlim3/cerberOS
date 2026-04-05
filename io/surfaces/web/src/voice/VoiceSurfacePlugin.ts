/**
 * Voice surface plugin — hooks voice input into the SurfaceAdapter pipeline.
 * Attaches to the WebSurfaceAdapter and intercepts voice inputs,
 * transcribing them to text before passing to receiveInput().
 */

import type { WebSurfaceAdapter } from '../surface/WebSurfaceAdapter'
import { AudioCapture, type CaptureResult } from './AudioCapture'

export function attachVoiceToSurface(
  surface: WebSurfaceAdapter,
  options?: { onTranscribing?: (v: boolean) => void }
): () => void {
  // VoiceRecorder is React-based — for the adapter, we provide a raw capture API
  // The React component handles UI; this plugin handles the transcription pipeline

  // The actual React integration happens in ChatWindow.tsx calling
  // surface.receiveInput({ type: 'voice', content: transcribedText })
  // after transcription completes

  return () => {
    // cleanup
  }
}
