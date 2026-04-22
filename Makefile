BINARY_NAME := dcm-kcli-provider

build:
	go build -o bin/$(BINARY_NAME) ./cmd/$(BINARY_NAME)

test:
	go run github.com/onsi/ginkgo/v2/ginkgo -r --race

test-cover:
	go run github.com/onsi/ginkgo/v2/ginkgo -r --race --cover

lint:
	golangci-lint run ./...

check: lint test

fmt:
	go fmt ./...

vet:
	go vet ./...

clean:
	rm -rf bin/

.PHONY: build test test-cover lint check fmt vet clean
