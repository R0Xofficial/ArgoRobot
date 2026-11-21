# Argo (Go). Build with:  docker build -t argorobot .
# Change GO_VERSION to the Go you use locally if the build asks for a newer toolchain.
ARG GO_VERSION=1.27
FROM golang:${GO_VERSION} AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download || true
COPY cmd ./cmd
COPY internal ./internal
# go.sum is made by `go mod tidy` (see README.md); building without it still works, it is only slower to verify.
RUN go mod tidy && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/argo ./cmd/argo
RUN mkdir -p /out/data   # the MTProto session lives here (a volume)

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/argo /app/argo
COPY --from=build --chown=65532:65532 /out/data /app/data
COPY locales /app/locales
ENV LOCALES_DIR=/app/locales
USER nonroot
ENTRYPOINT ["/app/argo"]
