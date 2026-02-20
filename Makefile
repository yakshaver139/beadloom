.PHONY: build test clean install lint

BINARY := beadloom
BUILD_DIR := ./cmd/beadloom

build:
	go build -o $(BINARY) $(BUILD_DIR)

install:
	go install $(BUILD_DIR)

test:
	go test ./...

test-v:
	go test -v ./...

lint:
	go vet ./...

clean:
	rm -f $(BINARY)
	rm -rf .worktrees/
	rm -rf .beadloom/
