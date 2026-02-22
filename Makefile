# Sync version from main.go (single source of truth)
VERSION := $(shell sed -n 's/.*version.*= *"\(.*\)"/\1/p' cmd/sift/main.go | head -1)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME ?= $(shell date -u '+%Y-%m-%d_%H:%M:%S')

LDFLAGS = -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_TIME)

# Release
DIST     := dist
BINNAME  := sift
TARGETS  := darwin-arm64 darwin-amd64

# golangci-lint version
GOLANGCI_LINT_VERSION ?= v1.62.2

.PHONY: build install clean test lint vet check setup fmt dist release changelog-check tag

build:
	go build -ldflags "$(LDFLAGS)" -o sift ./cmd/sift

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/sift

clean:
	rm -f sift
	rm -rf $(DIST)

test:
	go test -race ./...

test-verbose:
	go test -v -race ./...

test-cover:
	go test -cover ./...

# Format code
fmt:
	gofmt -w .
	@command -v goimports >/dev/null 2>&1 && goimports -w . || true

# Run go vet
vet:
	go vet ./...

# Run golangci-lint (checks GOPATH/bin if not in PATH)
GOLANGCI_LINT := $(shell command -v golangci-lint 2>/dev/null || echo "$(shell go env GOPATH)/bin/golangci-lint")
lint:
	@test -x "$(GOLANGCI_LINT)" || { echo "golangci-lint not found. Run 'make setup' first."; exit 1; }
	$(GOLANGCI_LINT) run

# Run all checks (vet + lint + test)
check: vet lint test
	@echo "All checks passed!"

# Install development tools
setup:
	@echo "Installing golangci-lint $(GOLANGCI_LINT_VERSION)..."
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	@echo "Installing goimports..."
	go install golang.org/x/tools/cmd/goimports@latest
	@echo "Setup complete! Tools installed to $$(go env GOPATH)/bin"
	@echo "Make sure $$(go env GOPATH)/bin is in your PATH"

# Show what version would be built
version:
	@echo "Version: $(VERSION)"
	@echo "Commit:  $(COMMIT)"
	@echo "Built:   $(BUILD_TIME)"

# --- Release ---

# Build macOS binaries for both architectures
dist: clean
	@echo "Building v$(VERSION) for macOS..."
	@for target in $(TARGETS); do \
		os=$$(echo $$target | cut -d- -f1); \
		arch=$$(echo $$target | cut -d- -f2); \
		dir=$(DIST)/$(BINNAME)-$(VERSION)-$$target; \
		mkdir -p $$dir; \
		GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o $$dir/$(BINNAME) ./cmd/sift; \
		cp LICENSE README.md CHANGELOG.md $$dir/; \
		tar -czf $(DIST)/$(BINNAME)-$(VERSION)-$$target.tar.gz -C $(DIST) $(BINNAME)-$(VERSION)-$$target; \
		rm -rf $$dir; \
		echo "  -> $(DIST)/$(BINNAME)-$(VERSION)-$$target.tar.gz"; \
	done

# Extract changelog for current version
RELEASE_NOTES := $(DIST)/release-notes.md
changelog-check:
	@if ! grep -q "## \[$(VERSION)\]" CHANGELOG.md; then \
		echo "ERROR: No changelog entry for v$(VERSION) in CHANGELOG.md"; \
		exit 1; \
	fi

# Create git tag for current version
tag: changelog-check
	@if git rev-parse "v$(VERSION)" >/dev/null 2>&1; then \
		echo "Tag v$(VERSION) already exists"; \
	else \
		git tag -a "v$(VERSION)" -m "Release v$(VERSION)"; \
		echo "Created tag v$(VERSION)"; \
	fi

# Extract release notes from CHANGELOG.md for current version
$(RELEASE_NOTES): changelog-check
	@mkdir -p $(DIST)
	@awk '/^## \[$(VERSION)\]/{found=1; next} found && /^## \[/{exit} found{print}' CHANGELOG.md > $(RELEASE_NOTES)

# Full release: check, build, tag, push, create GitHub release
release: check dist tag $(RELEASE_NOTES)
	git push origin "v$(VERSION)"
	gh release create "v$(VERSION)" $(DIST)/*.tar.gz \
		--title "v$(VERSION)" \
		--notes-file $(RELEASE_NOTES)
	@echo "Released v$(VERSION)!"

# Help
help:
	@echo "Available targets:"
	@echo "  setup        - Install dev tools (golangci-lint, goimports)"
	@echo "  build        - Build the binary"
	@echo "  install      - Install to GOPATH/bin"
	@echo "  test         - Run tests"
	@echo "  test-verbose - Run tests with -v -race"
	@echo "  test-cover   - Run tests with coverage"
	@echo "  fmt          - Format code (gofmt + goimports)"
	@echo "  vet          - Run go vet"
	@echo "  lint         - Run golangci-lint"
	@echo "  check        - Run all checks (vet + lint + test)"
	@echo "  clean        - Remove built binary and dist/"
	@echo "  version      - Show build version info"
	@echo ""
	@echo "Release:"
	@echo "  dist         - Build macOS binaries (arm64 + amd64)"
	@echo "  tag          - Create git tag from version in main.go"
	@echo "  release      - Full release: check + dist + tag + gh release"
