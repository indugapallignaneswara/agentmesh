# Multi-stage build: a static binary on a distroless base. No shell, no package
# manager, non-root — the smallest sensible attack surface for a server that is
# meant to be exposed.

FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache deps separately from sources.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
# CGO off => a fully static binary that runs on distroless/scratch.
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/agentmesh ./cmd/agentmesh && \
    CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w" \
    -o /out/coord ./cmd/coord && \
    CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w" \
    -o /out/agentiam ./cmd/agentiam

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/agentmesh /usr/local/bin/agentmesh
COPY --from=build /out/coord /usr/local/bin/coord
COPY --from=build /out/agentiam /usr/local/bin/agentiam

USER nonroot:nonroot
EXPOSE 8080

# The image runs the server; `docker run --entrypoint coord ...` for the CLI,
# `--entrypoint agentiam` for the Agent-IAM authorization server (listens on
# :8090 by default), and `agentmesh token ...` for credential admin.
ENTRYPOINT ["/usr/local/bin/agentmesh"]
