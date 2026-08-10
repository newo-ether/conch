.PHONY: all build-dir build-linux-arm64 build-linux-amd64 build-windows-amd64 mcp-all mcp-linux-arm64 mcp-linux-amd64 mcp-windows-amd64 clean

VERSION ?= dev
REVISION ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell git show -s --format=%cI HEAD 2>/dev/null || echo unknown)
LDFLAGS = -s -w -X github.com/newo-ether/conch/buildinfo.Version=$(VERSION) -X github.com/newo-ether/conch/buildinfo.Revision=$(REVISION) -X github.com/newo-ether/conch/buildinfo.BuildTime=$(BUILD_TIME)

all: build-linux-arm64 build-linux-amd64 build-windows-amd64

mcp-all: mcp-linux-arm64 mcp-linux-amd64 mcp-windows-amd64

build-dir:
	mkdir -p build

build-linux-arm64: build-dir
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="$(LDFLAGS)" -o build/conch-linux-arm64 .

build-linux-amd64: build-dir
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="$(LDFLAGS)" -o build/conch-linux-amd64 .

build-windows-amd64: build-dir
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="$(LDFLAGS)" -o build/conch-windows-amd64.exe .

mcp-linux-arm64: build-dir
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="$(LDFLAGS)" -o build/conch-mcp-linux-arm64 ./cmd/mcp

mcp-linux-amd64: build-dir
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="$(LDFLAGS)" -o build/conch-mcp-linux-amd64 ./cmd/mcp

mcp-windows-amd64: build-dir
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="$(LDFLAGS)" -o build/conch-mcp-windows-amd64.exe ./cmd/mcp

clean:
	rm -rf build/
