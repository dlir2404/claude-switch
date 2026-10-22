PREFIX ?= $(HOME)/.local/bin
SWIFTC ?= /usr/bin/swiftc
GO ?= go

.PHONY: all build build-bar install clean test

all: build build-bar

build:
	@mkdir -p bin
	$(GO) build -ldflags="-s -w" -o bin/claude-switch cmd/claude-switch/main.go

build-bar:
	@mkdir -p bin
	$(SWIFTC) -O -target arm64-apple-macos13.0 \
		-framework AppKit -framework UserNotifications \
		cmd/claude-switch-bar/main.swift \
		-o bin/claude-switch-bar

install: all
	@mkdir -p $(PREFIX)
	cp bin/claude-switch $(PREFIX)/claude-switch
	cp bin/claude-switch-bar $(PREFIX)/claude-switch-bar
	@echo "✓ Đã cài đặt claude-switch và claude-switch-bar vào $(PREFIX)"

clean:
	rm -rf bin

test:
	$(GO) test ./...
