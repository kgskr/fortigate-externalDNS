# Multi-stage, multi-arch build. The builder runs on the native build platform
# and cross-compiles the static binary for the target platform (CGO disabled), so
# no QEMU emulation is needed; the distroless runtime stage only copies the
# binary. The runtime image is distroless static, non-root, with CA certificates
# for HTTPS FortiGate endpoints.
#
# Base images are pinned by multi-arch manifest-list digest (the tag remains as
# human-readable context); Dependabot's docker ecosystem keeps the digests fresh.
FROM --platform=$BUILDPLATFORM golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /out/fortigate-external-dns ./cmd/fortigate-external-dns

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/fortigate-external-dns /fortigate-external-dns
USER 65532:65532
ENTRYPOINT ["/fortigate-external-dns"]
