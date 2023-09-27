.PHONY: test
test:
	docker run -p 6379:6379 -e REDIS_ARGS="--health-interval 10s --health-timeout 5s --health-retries 5" --name redis-server --rm redis:6.2 &
	go test ./... -race -short -v -timeout 60s
	docker kill redis-server

.PHONY: build
build: 
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -v -o ./dist/redisstreams-source main.go

# todo: don't hardcode image tag here - instead use Git tag
.PHONY: image
image: build
	docker build -t "quay.io/numaio/numaflow-source/redisstreams-source-go:v0.1.0" --target redisstreams-source .

clean:
	-rm -rf ./dist