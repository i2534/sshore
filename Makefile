# sshore build targets (Wails v2). Cross-platform build via `wails build -platform`.
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
OUTNAME  := sshore

# --- size optimization ------------------------------------------------------
# Always strip symbols/DWARF (-s -w, via -ldflags) and file paths (-trimpath is
# a go-build-level flag, passed to `wails build` as -trimpath, NOT inside
# -ldflags — go build rejects it there): no runtime cost, small free saving.
# Version is injected from the latest git tag so the 帮助/About panel can show it.
VERSION   := $(shell git describe --tags 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.Version=$(VERSION)
TRIMPATH  := -trimpath
# UPX-compress the final binaries (~63% smaller). Default on; set COMPRESS=0
# to disable (e.g. to avoid antivirus false positives or for faster startup).
COMPRESS ?= 1
ifeq ($(COMPRESS),1)
UPX_OPT  := -upx
else
UPX_OPT  :=
endif

.PHONY: help all dev build run linux windows clean test vet fmt e2e ci

## Show available targets and their descriptions.
help:
	@awk 'BEGIN{printf "  %-12s %s\n", "Target", "Description"} /^## /{if(d=="")d=substr($$0,4); next} /^[A-Za-z_][A-Za-z0-9_.-]*:.*=/{next} /^[A-Za-z_][A-Za-z0-9_.-]*:/{printf "  \033[36m%-12s\033[0m %s\n", substr($$1,1,length($$1)-1), d; d=""}' $(MAKEFILE_LIST)

## Build for the current platform.
all: build

## Run the app in dev mode (hot reload).
dev:
	$(WAILS) dev

## Build for the current platform (with webkit tag on linux; no re-package).
build:
	$(WAILS) build -skipbindings -clean -nopackage -ldflags "$(LDFLAGS)" $(TRIMPATH) $(UPX_OPT) $(if $(WEBKIT_TAG),-tags $(WEBKIT_TAG),)

## Build and run the app for the current platform's binary.
run: build
	./$(BINDIR)/$(OUTNAME)

## Build Linux amd64 binary.
linux:
	$(WAILS) build -platform linux/amd64 -nopackage -skipbindings -ldflags "$(LDFLAGS)" $(TRIMPATH) $(UPX_OPT) -tags $(WEBKIT_TAG)

## Build Windows amd64 binary (packaged: embeds icon via .syso resource).
## NOTE: do NOT pass -nopackage here — Wails only generates the icon-bearing
## .syso resource when Pack is true (see pkg/commands/build/build.go).
windows:
	$(WAILS) build -platform windows/amd64 -skipbindings -ldflags "$(LDFLAGS)" $(TRIMPATH) $(UPX_OPT)

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

## Run e2e script (throwaway local sshd; needs /usr/sbin/sshd, ssh-keygen, python3).
e2e:
	bash e2e/test_local.sh

## Run everything CI runs: vet + Go tests (-race) + frontend tests.
ci:
	$(GO) vet ./...
	$(GO) test ./... -race -count=1
	cd frontend && npx vitest run
