IMAGE ?= localhost/fortigate-external-dns:dev
GOOS ?= linux
GOARCH ?= $(shell go env GOARCH)

.PHONY: test static fmt-check build image helm-template smoke secret-scan validate

test:
	go test ./...

static:
	go vet ./...

fmt-check:
	@unformatted=$$(gofmt -l $$(git ls-files '*.go')); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi

secret-scan:
	./scripts/secret-scan.sh

build:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags="-s -w" -o bin/fortigate-external-dns ./cmd/fortigate-external-dns

image:
	podman build -f Containerfile -t $(IMAGE) .

helm-template:
	./scripts/helm-template-check.sh

smoke:
	go test ./internal/controller -run TestDryRunSmoke -v

validate: fmt-check test static helm-template image smoke secret-scan
