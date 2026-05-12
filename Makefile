.PHONY: build install clean run

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -ldflags "-X main.Version=$(VERSION)" -o git-assist .

install: build
	mkdir -p $(HOME)/.local/bin
	cp git-assist $(HOME)/.local/bin/
	@if [ "$$(uname -s)" = "Darwin" ]; then codesign -s - $(HOME)/.local/bin/git-assist; fi

clean:
	rm -f git-assist

run: build
	./git-assist
