.PHONY: build test clean install lint build-ui

build-ui:
	cd beadloom_visualiser && npm ci && npm run build
	rm -rf internal/viewer/dist && mkdir -p internal/viewer/dist
	cp -r beadloom_visualiser/dist/* internal/viewer/dist/

build:
	go build -o beadloom ./cmd/beadloom
	go build -o bdl ./cmd/bdl

install:
	go install ./cmd/beadloom
	go install ./cmd/bdl

test:
	go test ./...

test-v:
	go test -v ./...

lint:
	go vet ./...

clean:
	rm -f beadloom bdl
	rm -rf .worktrees/
	rm -rf .beadloom/
