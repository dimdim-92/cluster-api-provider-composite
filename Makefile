.DEFAULT_GOAL := verify
SHELL := /bin/sh
include hack/tools.env

GO ?= go
GOFLAGS ?= -mod=readonly
GOLANGCI_LINT ?= golangci-lint
KUBE_API_LINTER := $(CURDIR)/.tools/kube-api-linter/golangci-lint-kal
ACTIONLINT ?= $(CURDIR)/.tools/actionlint/$(ACTIONLINT_VERSION)/actionlint
GITLEAKS ?= $(CURDIR)/.tools/gitleaks/$(GITLEAKS_VERSION)/gitleaks
GOVULNCHECK ?= $(CURDIR)/.tools/govulncheck/$(GOVULNCHECK_VERSION)/govulncheck
HELM ?= helm
KUSTOMIZE ?= $(GO) run sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION)
CONTROLLER_GEN ?= $(GO) run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)
SETUP_ENVTEST := $(CURDIR)/.tools/$(SETUP_ENVTEST_VERSION)/setup-envtest
KUBEBUILDER_ASSETS ?=
COVERPROFILE ?=
# x-release-please-start-version
VERSION ?= v0.1.0
# x-release-please-end
IMAGE ?= ghcr.io/numspot/cluster-api-provider-composite:$(VERSION)
PLATFORM ?= linux/amd64
export VERSION IMAGE KUSTOMIZE GO HELM GOLANGCI_LINT

.PHONY: all verify gen verify-generated verify-crd-labels fmt lint lint-fix test test-api test-envtest setup-envtest envtest-download build docker-build docker-build-multiarch release-manifests helm-package verify-packaging run
.PHONY: verify-tools verify-headers headers-fix verify-api-deps
.PHONY: lint-api
.PHONY: verify-hygiene verify-vulnerabilities

all: verify

# Verification does not generate into or fix the working tree.
verify: verify-tools
	$(MAKE) verify-headers verify-api-deps verify-generated verify-crd-labels lint lint-api test verify-packaging verify-vulnerabilities

verify-tools:
	sh hack/verify-tools.sh

verify-headers:
	$(GO) run ./hack/verify-headers

headers-fix:
	$(GO) run ./hack/verify-headers -fix

verify-api-deps:
	sh hack/verify-api-deps.sh

gen:
	$(GO) generate ./...
	cd api && $(GO) generate ./...
	$(GO) run ./hack/crd-labels ./api/config/crd/bases
	$(CONTROLLER_GEN) rbac:roleName=composite-manager-role paths=./internal/... output:rbac:dir=config/rbac
	mkdir -p helm/crds
	cp api/config/crd/bases/*.yaml helm/crds/
	$(GO) run ./hack/render-rbac config/rbac/role.yaml helm/templates/_generated-rbac.tpl

verify-generated:
	sh hack/verify-generated.sh

verify-crd-labels:
	sh hack/verify-crd-labels.sh

fmt:
	$(GOLANGCI_LINT) fmt
	cd api && $(GOLANGCI_LINT) fmt

lint: verify-tools
	$(GOLANGCI_LINT) run --path-mode abs
	cd api && $(GOLANGCI_LINT) run --path-mode abs

lint-fix:
	$(GOLANGCI_LINT) run --fix --path-mode abs
	cd api && $(GOLANGCI_LINT) run --fix --path-mode abs

$(KUBE_API_LINTER): .custom-gcl.yml hack/tools.env
	$(GOLANGCI_LINT) custom

lint-api: verify-tools $(KUBE_API_LINTER)
	cd api && $(KUBE_API_LINTER) run --config ../.golangci-kal.yml ./v1beta2/...

$(ACTIONLINT): hack/tools.env
	mkdir -p "$(dir $(ACTIONLINT))"
	GOBIN="$(dir $(ACTIONLINT))" $(GO) install github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

$(GITLEAKS): hack/tools.env
	mkdir -p "$(dir $(GITLEAKS))"
	GOBIN="$(dir $(GITLEAKS))" $(GO) install github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION)

verify-hygiene: $(ACTIONLINT) $(GITLEAKS)
	GITLEAKS="$(GITLEAKS)" $(GO) test $(GOFLAGS) -race -timeout 120s ./hack -run '^TestHygiene'
	ACTIONLINT="$(ACTIONLINT)" GITLEAKS="$(GITLEAKS)" bash hack/verify-hygiene.sh

$(GOVULNCHECK): hack/tools.env
	mkdir -p "$(dir $(GOVULNCHECK))"
	GOBIN="$(dir $(GOVULNCHECK))" $(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

verify-vulnerabilities: $(GOVULNCHECK)
	$(GOVULNCHECK) ./...
	cd api && $(GOVULNCHECK) ./...

test: setup-envtest
	@set -eu; assets="$(KUBEBUILDER_ASSETS)"; \
	if [ -z "$$assets" ]; then assets="$$($(SETUP_ENVTEST) use -i $(ENVTEST_VERSION) -p path)"; fi; \
	KUBEBUILDER_ASSETS="$$assets" $(GO) test $(GOFLAGS) -race -timeout 300s $(if $(COVERPROFILE),-coverprofile=$(COVERPROFILE)) ./... $(if $(TEST),-run $(TEST))
	$(MAKE) test-api

test-api:
	cd api && $(GO) test $(GOFLAGS) -race $(if $(COVERPROFILE),-coverprofile=$(COVERPROFILE)) ./...

test-envtest: test

setup-envtest:
	@test -x "$(SETUP_ENVTEST)" || { \
		mkdir -p "$(dir $(SETUP_ENVTEST))"; \
		GOBIN="$(dir $(SETUP_ENVTEST))" $(GO) install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(SETUP_ENVTEST_VERSION); \
	}

envtest-download: setup-envtest
	$(SETUP_ENVTEST) use $(ENVTEST_VERSION)

build:
	sh hack/verify-version.sh
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -trimpath -ldflags "-X main.Version=$$VERSION" -o bin/manager ./cmd/operator

docker-build:
	sh hack/verify-version.sh
	docker build --platform "$(PLATFORM)" -f build/operator.Dockerfile --build-arg "version=$$VERSION" -t "$$IMAGE" .

# Produce a local multi-architecture archive; this target never pushes an image.
docker-build-multiarch:
	sh hack/verify-version.sh
	mkdir -p out
	docker buildx build --platform linux/amd64,linux/arm64 -f build/operator.Dockerfile --build-arg "version=$$VERSION" -t "$$IMAGE" --output type=oci,dest=out/composite-image.tar .

release-manifests:
	sh hack/release-manifests.sh

helm-package:
	sh hack/helm-package.sh

verify-packaging:
	$(HELM) lint helm
	$(KUSTOMIZE) build config/default >/dev/null
	$(HELM) template composite helm --namespace composite-system --include-crds >/dev/null

run:
	tilt up
