# loon
#
# Everything runs the Go toolchain in a container (scripts/go.sh explains why).
# `make help` lists the targets.
#
# GO=go runs the host toolchain instead, which is what CI does: a clean Linux
# container has no anti-virus to work around, and nesting one container inside
# another buys nothing. The checks themselves are identical either way.

GO ?= bash scripts/go.sh

# gofmt is a separate BINARY, not a go subcommand, so it cannot go through $(GO)
# unchanged. scripts/go.sh takes `gofmt` as its first word and runs the binary;
# the bare toolchain needs the binary named directly, because `go gofmt` is not
# a command.
#
# That distinction is not pedantic. Before this variable existed the target ran
# `go gofmt -l .` under GO=go, which fails to stderr and prints nothing to
# stdout — so the captured output was empty, the emptiness read as "no files
# need formatting", and the target reported CLEAN. A check that cannot run and
# says it passed is worse than no check.
GOFMT ?= $(if $(filter go,$(GO)),gofmt,$(GO) gofmt)

.PHONY: help test vet fmt sqllint tidy check

## help: list the targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'

## test: the whole suite
##
## There is no integration target here and none is needed: loon depends on no
## database. It DEFINES the storage seam (core.Storage) and the host supplies
## it, so every test in this repo is a unit test and `make check` is the whole
## of what CI can run.
## The coverage profile is written every run and gitignored (*.out). CI turns it
## into a job summary, which is the only place a library's coverage is visible
## to somebody deciding whether to depend on it.
test:
	$(GO) test -coverprofile=coverage.out ./...

## vet: go vet
vet:
	$(GO) vet ./...

## fmt: report anything gofmt would change (an error, not a suggestion)
##
## Reliable here, unlike the sibling repos' copies of this target. git is
## configured with core.autocrlf=true on Windows, which normally leaves the
## working tree full of CRLF files that gofmt wants to rewrite — but this
## checkout is LF throughout, so the list means what it says. If that ever
## changes, the answer that counts is CI on Linux against the stored LF copies;
## the local one is `git diff`, which compares normalised content.
fmt:
	@out=$$($(GOFMT) -l .) || { echo "gofmt could not run: $(GOFMT)"; exit 1; }; \
	 if [ -n "$$out" ]; then echo "gofmt would change:"; echo "$$out"; exit 1; fi; \
	 echo "gofmt: clean"

## sqllint: the constant-only-SQL guard (scripts/lint-sql)
##
## loon runs no SQL of its own beyond migrations, so this looks thin. It is
## here because the linter LIVES here and the sibling repos run it against
## themselves: a change to the linter that stops it detecting anything would
## pass silently in the only repo that can catch it.
sqllint:
	$(GO) run ./scripts/lint-sql ./...

## tidy: go.mod and go.sum must already be what `go mod tidy` produces
##
## A stray requirement or a missing sum is invisible until somebody clones
## fresh — the local module cache papers over both, so it fails for the
## newcomer and nobody else.
tidy:
	@cp go.mod /tmp/go.mod.bak; cp go.sum /tmp/go.sum.bak; \
	 $(GO) mod tidy; \
	 if ! diff -u /tmp/go.mod.bak go.mod || ! diff -u /tmp/go.sum.bak go.sum; then \
	   echo "go.mod/go.sum are not tidy — run 'go mod tidy' and commit the result"; \
	   cp /tmp/go.mod.bak go.mod; cp /tmp/go.sum.bak go.sum; exit 1; \
	 fi; \
	 echo "go.mod: tidy"

## check: everything CI runs
check: fmt vet sqllint test
