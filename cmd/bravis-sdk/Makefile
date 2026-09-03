.PHONY: build build-all clean test help

VERSION := 0.1.0
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)"

help:
	@echo "Bravis CLI Build"
	@echo ""
	@echo "Targets:"
	@echo "  make build       - Build for current OS"
	@echo "  make build-all   - Build for all platforms"
	@echo "  make test        - Run tests"
	@echo "  make clean       - Remove binaries"

build:
	go build $(LDFLAGS) -o bravis-sdk-sdk .
	@echo "✓ Built: ./bravis-sdk"

build-all: build-darwin build-linux build-windows
	@echo "✓ Built all platforms"

build-darwin:
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o bravis-sdk-darwin-amd64 .

build-linux:
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bravis-sdk-linux-amd64 .

build-windows:
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o bravis-sdk-windows-amd64.exe .

test:
	go test -v ./...

install:
	go install $(LDFLAGS) .
	@echo "✓ Installed: bravis"

clean:
	rm -f bravis bravis-*

run:
	go run . extract https://example.com/data.csv
