.PHONY: all build-linux-arm64 build-linux-amd64 build-windows-amd64 clean

all: build-linux-arm64 build-linux-amd64 build-windows-amd64

build-linux-arm64:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o build/conch-linux-arm64 .

build-linux-amd64:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o build/conch-linux-amd64 .

build-windows-amd64:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o build/conch-windows-amd64.exe .

clean:
	rm -rf build/
