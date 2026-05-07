# =========================
# Stage 1: Base & Dependencies
# =========================
FROM oven/bun:1-alpine AS base
WORKDIR /usr/src/app

FROM base AS install
RUN mkdir -p /temp/dev
COPY package.json bun.lock* /temp/dev/ 
RUN cd /temp/dev && bun install --frozen-lockfile

# =========================
# Stage 2: Build
# =========================
FROM base AS build
# Copy node_modules from the install stage
COPY --from=install /temp/dev/node_modules node_modules
COPY . .

# Run the build (outputs to ./dist or ./out usually)
RUN bun run build

# =========================
# Stage 3: Runtime (NGINX)
# =========================
FROM nginx:1.29-alpine AS runner
WORKDIR /usr/share/nginx/html

# Clean the directory
RUN rm -rf ./*

COPY --from=build /usr/src/app/out .

# Ensure your local nginx.conf exists
COPY ./nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]