.PHONY: build install clean run

build:
	go build -o git-assist .

install: build
	cp git-assist /usr/local/bin/

clean:
	rm -f git-assist

run: build
	./git-assist
