# syntax=docker/dockerfile:1.7
FROM node:22-bookworm-slim AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund

FROM node:22-bookworm-slim AS build
WORKDIR /app
ENV NEXT_TELEMETRY_DISABLED=1
COPY --from=deps /app/node_modules ./node_modules
COPY . .
RUN npm run build

FROM node:22-bookworm-slim AS runtime
RUN groupadd --system --gid 10001 marketradar && useradd --system --uid 10001 --gid marketradar --home-dir /app --shell /usr/sbin/nologin marketradar
WORKDIR /app
ENV NODE_ENV=production \
    NEXT_TELEMETRY_DISABLED=1 \
    HOSTNAME=127.0.0.1 \
    PORT=3000
COPY --from=build --chown=marketradar:marketradar /app/.next/standalone ./
COPY --from=build --chown=marketradar:marketradar /app/.next/static ./.next/static
USER marketradar
CMD ["node", "server.js"]
