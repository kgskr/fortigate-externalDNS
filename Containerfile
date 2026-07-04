# Multi-stage, multi-arch build. The builder runs on the native build platform
# and cross-compiles the static binary for the target platform (CGO disabled), so
# no QEMU emulation is needed; the distroless runtime stage only copies the
# binary. The runtime image is unchanged: distroless static, non-root, with CA
# certificates for HTTPS FortiGate endpoints.
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/fortigate-external-dns ./cmd/fortigate-external-dns

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/fortigate-external-dns /fortigate-external-dns
USER 65532:65532
ENTRYPOINT ["/fortigate-external-dns"]
