VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     = -s -w -X main.version=$(VERSION)
GO          = go
BINARY      = egent-jigsawstack
CMD         = ./cmd/agent
TARGETS     = linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build build-all test clean run version tag tag-patch tag-minor tag-major changelog help

build: ## Build for current platform
	@mkdir -p bin
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(CMD)

build-all: ## Cross-compile for all targets
	@mkdir -p dist
	@for target in $(TARGETS); do \
	  os=$$(echo $$target | cut -d/ -f1); \
	  arch=$$(echo $$target | cut -d/ -f2); \
	  echo "  building $$os/$$arch..."; \
	  GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 \
	    $(GO) build -trimpath -ldflags "$(LDFLAGS)" \
	      -o "dist/$(BINARY)-$$os-$$arch" $(CMD) || exit 1; \
	  tar -czf "dist/$(BINARY)-$$os-$$arch.tar.gz" \
	    -C dist "$(BINARY)-$$os-$$arch"; \
	  rm "dist/$(BINARY)-$$os-$$arch"; \
	  sha256sum "dist/$(BINARY)-$$os-$$arch.tar.gz" \
	    > "dist/$(BINARY)-$$os-$$arch.tar.gz.sha256"; \
	done
	@ls -lh dist/

test: ## Run all tests
	$(GO) test ./...

run: build ## Build and run (pass ARGS="...")
	./bin/$(BINARY) $(ARGS)

version: ## Print version
	@echo "$(VERSION)"

# --- Release helpers ---------------------------------------------------------

# Bump the patch component of the latest vX.Y.Z tag, e.g. v0.0.0 -> v0.0.1
tag-patch: ## Create next patch tag (v0.0.X)
	@$(MAKE) __tag-bump COMP=patch

tag-minor: ## Create next minor tag (v0.X.0)
	@$(MAKE) __tag-bump COMP=minor

tag-major: ## Create next major tag (vX.0.0)
	@$(MAKE) __tag-bump COMP=major

__tag-bump:
	@latest=$$(git tag --list 'v*' --sort=-v:refname | head -1); \
	  if [ -z "$$latest" ]; then latest="v0.0.0"; fi; \
	  ver=$$(echo $$latest | sed 's/^v//'); \
	  major=$$(echo $$ver | cut -d. -f1); \
	  minor=$$(echo $$ver | cut -d. -f2); \
	  patch=$$(echo $$ver | cut -d. -f3); \
	  case "$(COMP)" in \
	    patch) patch=$$((patch + 1));; \
	    minor) minor=$$((minor + 1)); patch=0;; \
	    major) major=$$((major + 1)); minor=0; patch=0;; \
	  esac; \
	  new="v$$major.$$minor.$$patch"; \
	  echo "  $$latest -> $$new"; \
	  git tag -a "$$new" -m "Release $$new"
	@echo "Push with: git push origin $$(git tag --list 'v*' --sort=-v:refname | head -1)"

clean: ## Remove build artifacts
	rm -rf bin/ dist/

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?##' Makefile | sort | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'
