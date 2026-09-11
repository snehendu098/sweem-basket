# syntax=docker/dockerfile:1
FROM oven/bun:1.3.11 AS deps
WORKDIR /app
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile

# Runtime needs `next` and react only — not typescript, eslint or the compiler.
FROM oven/bun:1.3.11 AS prod-deps
WORKDIR /app
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile --production

# Build + run on node: `bun run build` segfaults inside bun 1.3.11 on linux/arm64
# (Bun's own crash, not next's). bun is still what installs the lockfile.
FROM node:22-bookworm-slim AS build
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
# NEXT_PUBLIC_* are inlined into the bundle at BUILD time, not read at runtime.
# Changing them requires a rebuild (docker compose build client).
ARG NEXT_PUBLIC_PRIVY_APP_ID
ARG NEXT_PUBLIC_PRIVY_SIGNER_ID
ARG NEXT_PUBLIC_PRIVY_POLICY_ID
ARG NEXT_PUBLIC_WALLET_URL=http://localhost:8080
ARG NEXT_PUBLIC_MARKET_DATA_URL=http://localhost:8081
ARG NEXT_PUBLIC_CHAIN=Base
ENV NEXT_PUBLIC_PRIVY_APP_ID=$NEXT_PUBLIC_PRIVY_APP_ID \
    NEXT_PUBLIC_PRIVY_SIGNER_ID=$NEXT_PUBLIC_PRIVY_SIGNER_ID \
    NEXT_PUBLIC_PRIVY_POLICY_ID=$NEXT_PUBLIC_PRIVY_POLICY_ID \
    NEXT_PUBLIC_WALLET_URL=$NEXT_PUBLIC_WALLET_URL \
    NEXT_PUBLIC_MARKET_DATA_URL=$NEXT_PUBLIC_MARKET_DATA_URL \
    NEXT_PUBLIC_CHAIN=$NEXT_PUBLIC_CHAIN \
    NEXT_TELEMETRY_DISABLED=1
RUN ./node_modules/.bin/next build && rm -rf .next/cache

FROM node:22-bookworm-slim AS runner
WORKDIR /app
ENV NODE_ENV=production PORT=3000 NEXT_TELEMETRY_DISABLED=1
# standalone output already contains the pruned node_modules it needs
COPY --from=build /app/.next/standalone ./
COPY --from=build /app/.next/static ./.next/static
COPY --from=build /app/public ./public
COPY package.json next.config.ts ./
EXPOSE 3000
USER node
CMD ["node", "server.js"]
