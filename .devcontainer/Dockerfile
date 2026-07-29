FROM mcr.microsoft.com/devcontainers/go:1-1.22-bookworm

RUN apt-get update -o Acquire::AllowInsecureRepositories=true -o Acquire::AllowUnauthenticated=true || true \
    && apt-get install -y --no-install-recommends ffmpeg \
    && rm -rf /var/lib/apt/lists/*
