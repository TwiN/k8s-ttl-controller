BINARY=k8s-ttl-controller

.PHONY: build clean run test

build:
	go build -o $(BINARY) .

clean:
	rm $(BINARY)

run: build
	ENVIRONMENT=dev ./$(BINARY)

test:
	go test ./... -cover