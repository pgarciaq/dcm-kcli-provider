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

generate-api:
	go generate ./api/v1alpha1/...
	go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen --config=internal/api/server/server.gen.cfg -o internal/api/server/server.gen.go api/v1alpha1/openapi.yaml
	go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen --config=pkg/client/client.gen.cfg -o pkg/client/client.gen.go api/v1alpha1/openapi.yaml

check-generate-api: generate-api
	git diff --exit-code api/ internal/api/server/ pkg/client/ || \
		(echo "Generated files out of sync. Run 'make generate-api'." && exit 1)

check-aep:
	spectral lint --fail-severity=warn ./api/v1alpha1/openapi.yaml

clean:
	rm -rf bin/

.PHONY: build test test-cover lint check fmt vet generate-api check-generate-api check-aep clean
