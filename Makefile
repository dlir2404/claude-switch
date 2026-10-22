PREFIX ?= $(HOME)/.local/bin
SWIFTC ?= /usr/bin/swiftc
GO ?= go

.PHONY: all build build-bar install clean test

all: build build-bar

build:
	@mkdir -p bin
	$(GO) build -ldflags="-s -w" -o bin/claude-switch cmd/claude-switch/main.go

build-bar:
	@mkdir -p bin/claude-switch-bar.app/Contents/MacOS
	$(SWIFTC) -O -target arm64-apple-macos13.0 \
		-framework AppKit -framework UserNotifications \
		cmd/claude-switch-bar/main.swift \
		-o bin/claude-switch-bar.app/Contents/MacOS/claude-switch-bar
	cp cmd/claude-switch-bar/Info.plist bin/claude-switch-bar.app/Contents/Info.plist

install: all
	@mkdir -p $(PREFIX)
	cp bin/claude-switch $(PREFIX)/claude-switch
	ditto bin/claude-switch-bar.app $(PREFIX)/claude-switch-bar.app
	@echo "✓ Đã cài đặt claude-switch và claude-switch-bar vào $(PREFIX)"

clean:
	rm -rf bin

test:
	$(GO) test ./...
