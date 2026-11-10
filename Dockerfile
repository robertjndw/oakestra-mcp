# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=0.1.0
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/oakestra-mcp ./cmd/oakestra-mcp

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/oakestra-mcp /usr/local/bin/oakestra-mcp
# The activity log and the confirmation secret live here; the nonroot
# user (65532) must own it so a fresh named volume is writable.
COPY --from=build --chown=65532:65532 /tmp /data
VOLUME /data
EXPOSE 8090
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/oakestra-mcp"]
