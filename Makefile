IMAGE ?= localhost/fortigate-external-dns:dev
GOOS ?= linux
GOARCH ?= $(shell go env GOARCH)

.PHONY: test static build image helm-template smoke validate

test:
	go test ./...

static:
	go vet ./...

build:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags="-s -w" -o bin/fortigate-external-dns ./cmd/fortigate-external-dns

image: build
	podman build -f Containerfile -t $(IMAGE) .

helm-template:
	./scripts/helm-template-check.sh

smoke:
	go test ./internal/controller -run TestDryRunSmoke -v

validate: test static helm-template image smoke
