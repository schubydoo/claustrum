# Build claustrum the way the reference binary is built: CGO off, -trimpath,
# stripped (-s -w). Version/build-time are auto-embedded from git
# (vcs.revision/vcs.time), so -version reports the commit SHA.

BIN      := claustrum
DIST     := dist
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
LDFLAGS  := -s -w

.PHONY: build all clean fmt vet lint test hooks

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) .

all: clean
	@mkdir -p $(DIST)
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; ext=""; [ "$$os" = windows ] && ext=".exe"; \
	  out="$(DIST)/$(BIN)-$$os-$$arch$$ext"; \
	  echo "build $$out"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="$(LDFLAGS)" -o "$$out" . || exit 1; \
	done
	@echo "--- artifacts ---"; ls -l $(DIST)

fmt:
	gofmt -l -w *.go

vet:
	go vet ./...

lint:
	golangci-lint run ./...

test:
	go test -race ./...

# Install the repo's git hooks by pointing core.hooksPath at .githooks. One-time
# per clone; the hook then mirrors CI's lint job on every commit (see
# .githooks/pre-commit). Undo with `git config --unset core.hooksPath`.
hooks:
	git config core.hooksPath .githooks
	@echo "git hooks installed (core.hooksPath -> .githooks)"

clean:
	rm -rf $(DIST) $(BIN)
