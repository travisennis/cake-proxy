set positional-arguments

default:
    just --list

# Install this clone's git hooks: commit messages must be Conventional Commits.
hooks:
    git config core.hooksPath .githooks
    @echo "Installed .githooks (commit-msg)."

build:
    mkdir -p dist
    go build -trimpath -o dist/cake-proxy ./cmd/cake-proxy

version-check: build
    ./dist/cake-proxy -version | grep -q '^revision '

install:
    go install -trimpath ./cmd/cake-proxy

test:
    go test ./...

test-race:
    go test -race ./...

fmt:
    gofmt -w .

fmt-check:
    test -z "$(gofmt -l .)"

vet:
    go vet ./...

js-check:
    @if command -v node >/dev/null 2>&1; then \
        node --check internal/web/static/app.js; \
    else \
        echo "node not found; skipping JavaScript syntax check"; \
    fi

ci: fmt-check vet test js-check build version-check
