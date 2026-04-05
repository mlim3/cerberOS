# =============================================================================
# Stage 1: Build the web dashboard
# =============================================================================
FROM oven/bun:1 AS build-web

WORKDIR /app/web

# Copy web surface package files
COPY surfaces/web/package.json ./package.json
COPY surfaces/web/vite.config.ts ./vite.config.ts

# Install dependencies
RUN bun install --frozen-lockfile

# Copy all web source files
COPY surfaces/web/src ./src
COPY surfaces/web/index.html ./
COPY surfaces/web/tsconfig.json ./

# Build the web dashboard (tsc && vite build → dist/)
RUN bun run build

# =============================================================================
# Stage 2: Production runtime
# =============================================================================
FROM oven/bun:1 AS runtime

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

# Copy the full Bun workspace (needed for @cerberos/io-core workspace dep)
COPY package.json ./
COPY bun.lock ./
COPY core ./core
COPY api/package.json ./api/package.json
COPY api/tsconfig.json ./api/tsconfig.json
COPY api/src ./src

# Copy the pre-built web dist from stage 1
COPY --from=build-web /app/web/dist /app/web-dist

# Install all workspace dependencies
RUN bun install --frozen-lockfile

ENV NODE_ENV=production

EXPOSE 3001

CMD ["bun", "run", "src/index.ts"]
