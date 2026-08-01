PNPM ?= pnpm

.PHONY: install test build run dev clean
install:
	cd frontend && $(PNPM) install --frozen-lockfile

test:
	cd frontend && $(PNPM) typecheck && $(PNPM) test && $(PNPM) build
	go vet ./...
	go test ./...
	go test -race ./...

build:
	cd frontend && $(PNPM) build
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/tempo .

run: build
	./bin/tempo

dev:
	./scripts/dev.sh

clean:
	rm -rf frontend/dist bin
