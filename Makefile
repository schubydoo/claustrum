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
	gofmt -l -w *.go handlers internal

vet:
	go vet ./...

lint:
	golangci-lint run ./...
	GOOS=windows golangci-lint run ./...

# The time limit is explicit. The default of go test is 10 minutes. On one
# Linux host a race run took 552 s, and runs of a branch with more tests
# reached the default. A run past the default ends with a panic and no failed
# test.
#
# internal/git is the slowest package. Its tests run one at a time, so it
# runs as three go test processes at once, with the shard sets of the macOS
# legs of .github/workflows/ci.yml. A fourth process runs all other packages.
# Each process writes its own log, and the logs print at the end.
# scripts/check_git_shards.py checks that the three sets run each test once.
GIT_SHARD_WORKTREE := -run '^TestWorktree'
GIT_SHARD_STATUS   := -run '^(TestGitStatus|TestShadow|TestProbe|TestRepoHold)'
GIT_SHARD_REST     := -skip '^(TestWorktree|TestGitStatus|TestShadow|TestProbe|TestRepoHold)'

test:
	@pkgs=$$(go list ./... | grep -v '/internal/git$$'); \
	if [ -z "$$pkgs" ]; then echo "make test: go list found no packages"; exit 1; fi; \
	logs=$$(mktemp -d) || exit 1; \
	go test -race -count=1 -timeout 20m $(GIT_SHARD_WORKTREE) ./internal/git/ >"$$logs/1-git-worktree.log" 2>&1 & p1=$$!; \
	go test -race -count=1 -timeout 20m $(GIT_SHARD_STATUS) ./internal/git/ >"$$logs/2-git-status.log" 2>&1 & p2=$$!; \
	go test -race -count=1 -timeout 20m $(GIT_SHARD_REST) ./internal/git/ >"$$logs/3-git-rest.log" 2>&1 & p3=$$!; \
	go test -race -timeout 20m $$pkgs >"$$logs/4-other.log" 2>&1 & p4=$$!; \
	failed=""; \
	wait $$p1 || failed="$$failed git-worktree"; \
	wait $$p2 || failed="$$failed git-status"; \
	wait $$p3 || failed="$$failed git-rest"; \
	wait $$p4 || failed="$$failed other"; \
	for f in "$$logs"/*.log; do echo "=== $$(basename "$$f" .log)"; cat "$$f"; done; \
	rm -rf "$$logs"; \
	if [ -n "$$failed" ]; then echo "make test: FAIL in:$$failed"; exit 1; fi; \
	echo "make test: all four processes passed"

# Install the repo's git hooks by pointing core.hooksPath at .githooks. One-time
# per clone; the hook then mirrors CI's lint job on every commit (see
# .githooks/pre-commit). Undo with `git config --unset core.hooksPath`.
hooks:
	git config core.hooksPath .githooks
	@echo "git hooks installed (core.hooksPath -> .githooks)"

clean:
	rm -rf $(DIST) $(BIN)
