# =============================================================================
# Stage 1: Build the web dashboard
# =============================================================================
FROM oven/bun:1 AS build-web

WORKDIR /app

# Copy workspace root + all package.json files needed for resolution
COPY package.json ./
COPY bun.lock ./
COPY core/package.json ./core/package.json
COPY core/src ./core/src
COPY core/tsconfig.json ./core/tsconfig.json
COPY api/package.json ./api/package.json
COPY surfaces/web/package.json ./surfaces/web/package.json
COPY surfaces/cli/package.json ./surfaces/cli/package.json

# Install all workspace dependencies
RUN bun install

# Copy web source files
COPY surfaces/web/vite.config.ts ./surfaces/web/vite.config.ts
COPY surfaces/web/src ./surfaces/web/src
COPY surfaces/web/index.html ./surfaces/web/index.html
COPY surfaces/web/tsconfig.json ./surfaces/web/tsconfig.json

# Build the web dashboard (tsc && vite build → dist/)
RUN cd surfaces/web && bun run build

# =============================================================================
# Stage 2: Production runtime
# =============================================================================
FROM oven/bun:1 AS runtime

ARG ENABLE_VOICE=false

WORKDIR /app

# Voice / local STT: ffmpeg, Python, faster-whisper, and ~140MB Whisper model.
# Set ENABLE_VOICE=true at build time for full transcription (slow image build).
# Default false = smaller/faster builds; run with STT_PROVIDER=disabled (see docker-compose).
RUN if [ "$ENABLE_VOICE" = "true" ]; then \
  apt-get update && apt-get install -y ffmpeg \
  python3 python3-pip python3-venv \
  && rm -rf /var/lib/apt/lists/* \
  && python3 -m venv /opt/venv \
  && /opt/venv/bin/pip install --no-cache-dir faster-whisper \
  && /opt/venv/bin/python3 -c "from faster_whisper import download_model; download_model('base.en', output_dir='/app/whisper-models')"; \
  fi

# Copy the full Bun workspace (needed for @cerberos/io-core workspace dep)
COPY package.json ./
COPY bun.lock ./
COPY core ./core
COPY api/package.json ./api/package.json
COPY api/tsconfig.json ./api/tsconfig.json
COPY api/src ./api/src
COPY surfaces/web/package.json ./surfaces/web/package.json
COPY surfaces/cli/package.json ./surfaces/cli/package.json

# Copy the pre-built web dist from stage 1
COPY --from=build-web /app/surfaces/web/dist /app/web-dist

# Install all workspace dependencies
RUN bun install

ENV NODE_ENV=production

EXPOSE 3001

CMD ["bun", "run", "api/src/index.ts"]
