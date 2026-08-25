# sshkit build targets (Wails v2). Cross-platform build via `wails build -platform`.
#
# Linux native builds need webkit2gtk. This host only has webkit2gtk-4.1, so the
# linux target adds the `webkit2_41` build tag (Wails picks webkit2gtk-4.1 then).
# Pass WEBKIT_TAG= (or set it anywhere) to build against -4.0 instead.

SHELL := /bin/bash
GO     := go
WAILS  := $(shell $(GO) env GOPATH)/bin/wails

# Linux webkit: override with WEBKIT_TAG= to build against webkit2gtk-4.0.
WEBKIT_TAG ?= webkit2_41

BINDIR   := build/bin
OUTNAME  := sshkit

.PHONY: all dev build linux windows clean test vet fmt

## Build for the current platform.
all: build

## Run the app in dev mode (hot reload).
dev:
	$(WAILS) dev

## Build for the current platform (with webkit tag on linux; no re-package).
build:
	$(WAILS) build -skipbindings -clean $(if $(WEBKIT_TAG),-tags $(WEBKIT_TAG),)

## Build Linux amd64 binary.
linux:
	$(WAILS) build -platform linux/amd64 -nopackage -skipbindings -tags $(WEBKIT_TAG)

## Build Windows amd64 binary.
windows:
	$(WAILS) build -platform windows/amd64 -nopackage -skipbindings

## Build both Linux and Windows binaries (clean once, then both).
## NOTE: linux needs the webkit tag, windows does not, so these must run as
## separate `wails build` invocations sharing the same bin dir.
both: clean
	$(MAKE) linux
	$(MAKE) windows

## Remove build artifacts.
clean:
	rm -rf $(BINDIR)

## Run Go + frontend tests.
test:
	$(GO) test ./...
	cd frontend && npx vitest run

## Vet Go code.
vet:
	$(GO) vet ./...

## Format Go code (gofmt -s), excluding vendor/build output.
fmt:
	gofmt -s -w $$(find . -name '*.go' -not -path './vendor/*' -not -path '*/build/*' -not -path './node_modules/*')
