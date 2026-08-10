.PHONY: build test run clean

build:
	go build -o bin/agent-tag .

test:
	go test -race ./...

run:
	go run . web

clean:
	go clean
