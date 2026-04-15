#!/usr/bin/env python3
"""
CLI interface for the transcription subprocess.
Reads JSON from stdin, writes JSON to stdout.
Handles a single transcription request per invocation.
"""

import sys
import json
from pathlib import Path
import tempfile
import base64
import os
import shutil

# Ensure the transcription module is importable
sys.path.insert(0, str(Path(__file__).parent))

from model import transcribe


def main():
    try:
        request = json.loads(sys.stdin.readline().strip())

        audio_data = request.get("audioData")
        language = request.get("language")

        if not audio_data:
            print(json.dumps({"error": "audioData is required"}))
            sys.exit(1)

        # Decode base64 audio
        if audio_data.startswith("data:"):
            # Strip data URI prefix: "data:audio/webm;base64,..."
            audio_data = audio_data.split(",", 1)[1]

        audio_bytes = base64.b64decode(audio_data)

        # Write to temp file (ffmpeg reads from disk)
        tmp_dir = Path("/tmp/cerberOS-voice")
        tmp_dir.mkdir(exist_ok=True)

        # Determine format from data URI or default to webm
        # ffmpeg will auto-detect if format is ambiguous
        tmp_input = tmp_dir / f"input-{os.urandom(8).hex()}.webm"
        tmp_output = tmp_dir / f"input-{os.urandom(8).hex()}.wav"

        tmp_input.write_bytes(audio_bytes)

        # Convert to wav at 16kHz mono using ffmpeg
        import subprocess

        result = subprocess.run(
            [
                "ffmpeg",
                "-i", str(tmp_input),
                "-ar", "16000",
                "-ac", "1",
                "-c:a", "pcm_s16le",
                "-y",  # overwrite
                str(tmp_output),
            ],
            capture_output=True,
            text=True,
        )

        if result.returncode != 0:
            print(json.dumps({"error": f"ffmpeg failed: {result.stderr}"}))
            tmp_input.unlink(missing_ok=True)
            sys.exit(1)

        try:
            # Transcribe
            result = transcribe(str(tmp_output), language=language)
            print(json.dumps(result))
        finally:
            # Cleanup
            tmp_input.unlink(missing_ok=True)
            tmp_output.unlink(missing_ok=True)

    except Exception as e:
        print(json.dumps({"error": str(e)}), file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()