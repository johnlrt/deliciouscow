.PHONY: build test vet run

build:
	go build -o bin/cordova-server ./cmd/cordova-server

vet:
	go vet ./...

test: vet
	go test -race -v ./...

run:
	go run ./cmd/cordova-server
