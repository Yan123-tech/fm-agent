BINARY := fm-agent
PKG     := ./cmd/fm-agent
# ~/.local/bin is writable without sudo and on PATH for most shells;
# override with: make install PREFIX=/some/other/bin
PREFIX  ?= $(HOME)/.local/bin

.PHONY: build install uninstall test vet fmt

build:
	go build -o $(BINARY) $(PKG)

install: build
	install -m 0755 $(BINARY) "$(PREFIX)/$(BINARY)"
	@echo "installed $(PREFIX)/$(BINARY)"

uninstall:
	rm -f "$(PREFIX)/$(BINARY)"

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .
