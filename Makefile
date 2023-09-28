VERSION?=latest

.PHONY: test
test:
	docker run -p 6379:6379 -e REDIS_ARGS="--health-interval 10s --health-timeout 5s --health-retries 5" --name redis-server --rm redis &
	go test ./... -race -short -v -timeout 60s
	docker kill redis-server

.PHONY: build
build: 
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -v -o ./dist/redisstreams-source main.go

# todo: don't hardcode image tag here - instead use Git tag
.PHONY: image
image: build
	docker build -t quay.io/numaio/numaflow-source/redisstreams-source-go:$(VERSION) --target redisstreams-source .

$(GOPATH)/bin/golangci-lint:
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b `go env GOPATH`/bin v1.54.1

.PHONY: lint
lint: $(GOPATH)/bin/golangci-lint
	go mod tidy
	${GOPATH}/bin/golangci-lint run --fix --verbose --concurrency 4 --timeout 5m

clean:
	-rm -rf ./dist