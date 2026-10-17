# synk — сервер синхронизации Money Tracks.

GO        ?= go
# Проект без cgo (modernc.org/sqlite). Заодно обходит падение линкера на маке:
# ld из Command Line Tools не читает .tbd свежего SDK, любая cgo-линковка валится.
export CGO_ENABLED = 0
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE      := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG       := github.com/hobby-me/money-tracks-sync/internal/version
LDFLAGS   := -s -w \
  -X $(PKG).Version=$(VERSION) \
  -X $(PKG).GitCommit=$(COMMIT) \
  -X $(PKG).BuildDate=$(DATE)

.PHONY: build build-linux test bench run-tui docker

build:
	$(GO) build -ldflags '$(LDFLAGS)' -o bin/synk ./cmd/synk

build-linux:
	GOOS=linux GOARCH=amd64 $(GO) build \
	  -ldflags '$(LDFLAGS) -X $(PKG).BuildHash=$(shell git rev-parse --verify -q HEAD 2>/dev/null || echo unknown)' \
	  -o bin/synk-linux-amd64 ./cmd/synk

test:
	$(GO) vet ./...
	$(GO) test ./...

bench:
	$(GO) test -run XXX -bench . -benchmem -count=3 ./internal/store/

# Локальный прогон консоли на временных данных с примером учётки.
run-tui: build
	./scripts/demo-tui.sh

# Образ для docker compose (см. README, «Запуск в Docker»).
docker:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t synk:$(VERSION) -t synk:latest .

# Цели конкретной установки (деплой на свой сервер, логи, консоль по ssh) —
# в Makefile.local рядом; в общий репозиторий он не входит.
-include Makefile.local
