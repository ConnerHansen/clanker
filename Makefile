GIT_SHA := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")

.PHONY: build

dev:
	wails dev -tags webkit2_41

dev.cli:
	go run . --cli

tests:
	go test -v ./...

clean:
	rm -rf build/bin/

build:
	wails build -tags webkit2_41 -platform linux/amd64 -ldflags "-X main.Version=$(GIT_SHA)"

install: build
	mkdir -p $(HOME)/.local/bin
	cp build/bin/clanker $(HOME)/.local/bin/clanker

env.start:
	podman compose up

env.stop:
	podman compose down