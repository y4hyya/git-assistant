.PHONY: build install clean run

build:
	go build -o git-assist .

install: build
	mkdir -p $(HOME)/.local/bin
	cp git-assist $(HOME)/.local/bin/
	@if [ "$$(uname -s)" = "Darwin" ]; then codesign -s - $(HOME)/.local/bin/git-assist; fi

clean:
	rm -f git-assist

run: build
	./git-assist
