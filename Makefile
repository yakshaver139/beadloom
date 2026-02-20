.PHONY: build test clean install lint build-ui

BINARY := beadloom
BUILD_DIR := ./cmd/beadloom

build-ui:
	cd beadloom_visualiser && npm ci && npm run build
	rm -rf internal/viewer/dist && mkdir -p internal/viewer/dist
	cp -r beadloom_visualiser/dist/* internal/viewer/dist/

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
