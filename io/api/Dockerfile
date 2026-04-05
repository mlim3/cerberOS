FROM oven/bun:1 AS base
WORKDIR /app

# Install ffmpeg (audio format conversion)
RUN apt-get update && apt-get install -y ffmpeg && rm -rf /var/lib/apt/lists/*

# Install Python + faster-whisper
RUN apt-get update && apt-get install -y \
    python3 python3-pip python3-venv \
    && rm -rf /var/lib/apt/lists/*

# Install faster-whisper (Cython-compiled, no compilation needed at build time)
RUN python3 -m venv /opt/venv
RUN /opt/venv/bin/pip install faster-whisper

# Download whisper model at build time (base.en ~140MB)
# This avoids download on every container start
ENV WHISPER_MODEL=base.en
RUN /opt/venv/bin/python3 -c " \
    from faster_whisper import download_model; \
    download_model('$WHISPER_MODEL', output_dir='/app/whisper-models')"

# Copy Bun app
COPY package.json ./
RUN bun install --frozen-lockfile

COPY src/ ./src/

EXPOSE 3001

CMD ["bun", "run", "src/index.ts"]
