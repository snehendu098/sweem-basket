# syntax=docker/dockerfile:1
FROM rust:1-slim-bookworm AS build
WORKDIR /src

# Dependency build is a separate, cached layer: a stub main.rs compiles every
# crate once, and only src/ changes invalidate it.
COPY Cargo.toml Cargo.lock ./
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    mkdir src && echo 'fn main() {}' > src/main.rs && cargo build --release

COPY src ./src
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    touch src/main.rs && cargo build --release

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget && \
    rm -rf /var/lib/apt/lists/*
WORKDIR /app
# Read at startup; the executor refuses to start without it.
COPY venues.json ./venues.json
COPY --from=build /src/target/release/executor /usr/local/bin/executor
ENV VENUES_PATH=/app/venues.json EXECUTOR_ADDR=0.0.0.0:8082
EXPOSE 8082
USER nobody
ENTRYPOINT ["/usr/local/bin/executor"]
