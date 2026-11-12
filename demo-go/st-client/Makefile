.PHONY: build test test-live
build:
	mkdir -p ../../BINARIES
	GOWORK=off CGO_ENABLED=0 go build -trimpath -o ../../BINARIES/st-client .
test:
	GOWORK=off go test -race ./...
test-live:
	GOWORK=off ST_CLIENT_TEST_SERVER=../../BINARIES/st-web go test -race ./...
