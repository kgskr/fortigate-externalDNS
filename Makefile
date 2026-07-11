IMAGE ?= localhost/fortigate-external-dns:dev
GOOS ?= linux
GOARCH ?= $(shell go env GOARCH)
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: test static fmt-check build image helm-template platform-artifact-check platform-requirement-check docs-samples-check smoke secret-scan secret-scan-test release-workflow-check release-verification-test openspec-validate validate

test:
	go test ./...

static:
	go vet ./...

fmt-check:
	@unformatted=$$(gofmt -l $$(git ls-files '*.go')); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi

secret-scan:
	./scripts/secret-scan.sh

secret-scan-test:
	sh scripts/secret-scan_test.sh

release-workflow-check:
	sh scripts/release-workflow-check.sh
	sh scripts/release-workflow-check_test.sh

release-verification-test:
	sh scripts/verify-release-artifacts_test.sh

openspec-validate:
	openspec validate --specs --strict

build:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags="$(LDFLAGS)" -o bin/fortigate-external-dns ./cmd/fortigate-external-dns

image:
	podman build -f Containerfile --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t $(IMAGE) .

helm-template:
	./scripts/helm-template-check.sh

platform-artifact-check:
	ruby scripts/platform-artifact-check.rb

platform-requirement-check:
	ruby scripts/platform-requirement-check.rb

docs-samples-check:
	ruby scripts/docs-samples-check.rb
	sh -n samples/one-shot-plan.sh
	sh -n samples/release-verification.sh
	helm template docs-sample ./charts/fortigate-external-dns --values samples/monitoring-values.yaml >/dev/null

smoke:
	go test ./internal/controller -run TestDryRunSmoke -v

validate: fmt-check test static helm-template platform-artifact-check platform-requirement-check docs-samples-check openspec-validate image smoke secret-scan secret-scan-test release-workflow-check release-verification-test
