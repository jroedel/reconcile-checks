# The commands CLAUDE.md and the skills refer to. Seeded from drop-in forms'
# Makefile, and sion-backup's for the cross-builds.
#
# Anything added here gets a "## " comment on its target line, which is what
# "make help" reads.

GO                   := go
APP                  := reconcile-checks
TOOLS_DIR            := $(shell $(GO) env GOPATH)/bin
GOPLS_VERSION        ?= 0.23.0
STATICCHECK_VERSION  ?= 2026.1
GO_WINRES_VERSION    ?= 0.3.3

# The version stamped into a build. A release passes VERSION=vX.Y.Z
# (docs/10-install-update-and-releases.md); anything else is not a strict
# vX.Y.Z and so never updates itself.
VERSION              ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# The Windows build is a GUI program everywhere, CI included, so that what is
# tested is what ships (docs/02-architecture.md): no console window when it is
# opened from the Start menu.
LDFLAGS              := -s -w -X main.version=$(VERSION)
WINDOWS_LDFLAGS      := $(LDFLAGS) -H windowsgui

.DEFAULT_GOAL := help

.PHONY: help
help: ## List every target
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / \
		{ printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

# ---------------------------------------------------------------------------
# Navigation. Both of these are gopls, which answers from the same type
# information the compiler uses -- so it knows which "New" is a method on which
# type, and it brings the doc comment with it. grep is still right for a
# setting name, a SQL column or a string in a template; this is for the
# declaration of a Go identifier, which is the thing gopls is exact about.

.PHONY: sym
sym: ## Where is a Go symbol, and what is its declaration? (make sym NAME=Sessions.Mint)
	@scripts/sym "$(NAME)"

.PHONY: outline
outline: ## Every symbol in one file, with line numbers (make outline FILE=path/to/x.go)
	@if [ -z "$(FILE)" ]; then echo "usage: make outline FILE=path/to/file.go" >&2; exit 2; fi
	@command -v gopls >/dev/null || { echo "gopls is not installed. Run: make dev-tools" >&2; exit 1; }
	@# Rewritten into "file:line:col  name  kind", because gopls prints the
	@# position last and without the file -- and a position that is not a
	@# whole address is one somebody has to assemble by hand every time.
	@gopls symbols "$(FILE)" | awk -v f="$(FILE)" ' \
		{ \
			where = $$NF; sub(/-.*/, "", where); \
			$$NF = ""; sub(/ +$$/, "", $$0); \
			printf "%-52s %s\n", f ":" where, $$0 \
		} \
	'

.PHONY: dev-tools
dev-tools: ## Install the Go tooling this Makefile navigates and checks with (gopls, staticcheck)
	@# Pinned, so that two machines get the same answers.
	@GOBIN=$(TOOLS_DIR) $(GO) install golang.org/x/tools/gopls@v$(GOPLS_VERSION)
	@echo "installed $(TOOLS_DIR)/gopls ($$($(TOOLS_DIR)/gopls version | head -1))"
	@GOBIN=$(TOOLS_DIR) $(GO) install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)
	@echo "installed $(TOOLS_DIR)/staticcheck ($$($(TOOLS_DIR)/staticcheck -version))"
	@command -v gopls >/dev/null || echo "NOTE: $(TOOLS_DIR) is not on your PATH"

# ---------------------------------------------------------------------------
# The loop after touching Go, which is ardanlabs/kronk's and is followed here:
# format, vet, staticcheck, build, test -- every time, on the package that
# changed, fixing the code rather than suppressing the diagnostic.
#
# One package rather than the whole module. The module is new and clean, so
# the bound costs nothing today -- and the habit is in place before it is
# needed, which is the only time it can be adopted cheaply.

.PHONY: go-check
go-check: ## Format, vet, staticcheck, build and test one package (make go-check PKG=./app/sdk/mid)
	@if [ -z "$(PKG)" ]; then \
		echo "usage: make go-check PKG=./app/sdk/mid" >&2; \
		echo "       the package you changed, not ./... -- see the comment above this target" >&2; \
		exit 2; \
	fi
	@gofmt -s -w $(PKG)
	@$(GO) vet $(PKG)/...
	@# Under go.mod's Go release: staticcheck reads the compiler's export data,
	@# and a local toolchain newer than the one staticcheck was built for is
	@# one it can't read.
	@if command -v staticcheck >/dev/null; then \
		GOTOOLCHAIN=$(GO_MOD_RELEASE) staticcheck $(PKG)/...; \
	else \
		echo "staticcheck is not installed; run: make dev-tools"; exit 1; \
	fi
	@$(GO) build ./...
	@GO=$(GO) scripts/go-test $(PKG)/...

.PHONY: vet
vet: ## go vet the whole module, for Linux and for Windows
	$(GO) vet ./...
	GOOS=windows $(GO) vet ./...

.PHONY: fmt
fmt: ## Format all Go sources in place
	$(GO) fmt ./...

.PHONY: fmt-check
fmt-check: ## Fail, naming the files, if anything is not gofmt-clean
	@out=$$(gofmt -s -l .); \
	if [ -n "$$out" ]; then echo "not gofmt-clean:"; echo "$$out"; exit 1; fi

.PHONY: tidy-check
tidy-check: ## Fail if go.mod or go.sum is not tidy
	@cp go.mod go.mod.orig; cp go.sum go.sum.orig; \
	$(GO) mod tidy; \
	ok=1; cmp -s go.mod go.mod.orig && cmp -s go.sum go.sum.orig || ok=0; \
	mv go.mod.orig go.mod; mv go.sum.orig go.sum; \
	[ $$ok = 1 ] || { echo "go.mod or go.sum is not tidy; run: go mod tidy" >&2; exit 1; }

.PHONY: lint
lint: vet fmt-check ## vet (both systems) + gofmt check

# The Go release go.mod names, exactly. CI installs it (setup-go reads
# go.mod), so the vulnerability scan runs under it here too: a newer local
# toolchain has its own standard-library advisories, and a scan that is red in
# one place and green in the other, for a reason neither reports, is the worst
# shape for a check to fail in (drop-in forms learned this).
GO_MOD_RELEASE       := go$(shell awk '/^go /{print $$2}' go.mod)

.PHONY: vuln-check
vuln-check: ## Check dependencies against the Go vulnerability database, under go.mod's Go release (needs network)
	GOTOOLCHAIN=$(GO_MOD_RELEASE) $(GO) tool govulncheck ./...

.PHONY: test-unit
test-unit: ## Run unit tests, with the race detector
	@# -race needs cgo and a C compiler; CGO_ENABLED=0 is for the release
	@# build, not for tests.
	@#
	@# The passing packages are counted rather than listed, by scripts/go-test.
	@# Everything else is printed untouched. See that script for why the filter
	@# lives there and not in a pipeline somebody adds to each run by hand.
	@GO=$(GO) scripts/go-test -race ./...

.PHONY: test
test: test-unit lint ## Full local check: unit tests + lint

.PHONY: cover
cover: ## Unit tests with a coverage summary
	$(GO) test -cover ./...

.PHONY: tidy
tidy: ## Tidy go.mod
	$(GO) mod tidy

# ---------------------------------------------------------------------------
# Building and running.

.PHONY: build
build: ## Build the program for this computer
	$(GO) build -ldflags='$(if $(filter windows,$(shell $(GO) env GOOS)),$(WINDOWS_LDFLAGS),$(LDFLAGS))' -o $(APP)$(shell $(GO) env GOEXE) ./cmd/$(APP)

.PHONY: run
run: build ## Build and run against ./config.toml with its own data folder (never the real register)
	@[ -f config.toml ] || { \
		echo "config.toml does not exist. Start from the documented example:" >&2; \
		echo "    cp config.example.toml config.toml" >&2; \
		exit 1; \
	}
	./$(APP) --config config.toml

.PHONY: cross
cross: ## Type-check and build every target system, so a Windows-only break shows here
	@for target in linux/amd64 windows/amd64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		echo "  $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -o /dev/null ./... || exit 1; \
	done

.PHONY: release-all
release-all: ## Build every release binary into dist/ (VERSION=vX.Y.Z)
	@# CGO off is not a preference: the SQLite driver is pure Go, so each
	@# result is one file with nothing to install beside it, built for every
	@# system from one Linux runner.
	@rm -rf dist && mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		$(GO) build -trimpath -ldflags='$(LDFLAGS)' -o dist/$(APP)-linux-amd64 ./cmd/$(APP)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
		$(GO) build -trimpath -ldflags='$(WINDOWS_LDFLAGS)' -o dist/$(APP)-windows-amd64.exe ./cmd/$(APP)

.PHONY: checksums
checksums: ## SHA256SUMS of everything in dist/
	cd dist && sha256sum $$(ls | grep -v '^SHA256SUMS') > SHA256SUMS

# ---------------------------------------------------------------------------
# The icon (docs/12-look-and-voice.md). Drawn by scripts/icons from one
# description, into the PNGs and the SVG the pages use; then the Windows
# resource that gives the .exe, and so the Start menu shortcut, its icon.
# The results are committed: building the program needs none of this.

.PHONY: icons
icons: ## Redraw the icon, and remake the Windows resource that carries it
	$(GO) run ./scripts/icons
	$(GO) run github.com/tc-hib/go-winres@v$(GO_WINRES_VERSION) simply \
		--arch amd64 \
		--icon app/sdk/page/assets/img/icon-256.png \
		--manifest gui \
		--product-name "Reconcile Checks" \
		--file-description "Reconcile Checks" \
		--out cmd/$(APP)/rsrc
