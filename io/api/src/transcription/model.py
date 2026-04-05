"""
Loads and caches the faster-whisper model.
Model is loaded once on first transcription request, then reused.
"""

from functools import lru_cache
import os
from faster_whisper import WhisperModel

MODEL_DIR = os.environ.get("WHISPER_MODEL_DIR", "/app/whisper-models")
MODEL_NAME = os.environ.get("WHISPER_MODEL", "base.en")
DEVICE = os.environ.get("WHISPER_DEVICE", "cpu")

# compute_type: int8 on CPU is fast enough; use float16 on GPU
COMPUTE_TYPE = "int8" if DEVICE == "cpu" else "float16"


@lru_cache(maxsize=1)
def get_model():
    """Load model once and cache. Thread-safe after load."""
    model_path = os.path.join(MODEL_DIR, MODEL_NAME)
    if os.path.exists(model_path):
        # Custom model path
        return WhisperModel(model_path, device=DEVICE, compute_type=COMPUTE_TYPE)
    # Download on first use (only if no local model)
    return WhisperModel(MODEL_NAME, device=DEVICE, compute_type=COMPUTE_TYPE)


def transcribe(audio_path: str, language: str | None = None) -> dict:
    """
    Run transcription on an audio file.

    Returns dict:
        text: str — transcribed text
        language: str — detected or specified language
        duration: float — audio duration in seconds
    """
    model = get_model()
    segments, info = model.transcribe(
        audio_path,
        language=language,
        beam_size=5,
        vad_filter=True,  # voice activity detection
    )

    text = " ".join(seg.text for seg in segments)
    return {
        "text": text.strip(),
        "language": info.language or language or "en",
        "duration": info.duration or 0.0,
    }
