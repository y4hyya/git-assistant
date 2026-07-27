.PHONY: build install clean run test check fmt-check vet

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -ldflags "-X main.Version=$(VERSION)" -o git-assist .

# -race because the app runs git in Bubble Tea command goroutines while the
# update loop reads the model; -count=1 because a cached PASS from a previous
# working tree answers the wrong question.
test:
	go test -race -count=1 ./...

fmt-check:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needs to run on:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

vet:
	go vet ./...

# The same four steps, in the same order, as .github/workflows/ci.yml — change
# one and change the other, or CI stops meaning what `make check` means.
# (CI's compile step is `go build ./...`; `build` here compiles the same
# packages and additionally links the binary with the version ldflag.)
check: fmt-check vet build test

install: build
	mkdir -p $(HOME)/.local/bin
	cp git-assist $(HOME)/.local/bin/
	@if [ "$$(uname -s)" = "Darwin" ]; then codesign -s - $(HOME)/.local/bin/git-assist; fi

clean:
	rm -f git-assist

run: build
	./git-assist
