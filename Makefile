BINARY=k8s-ttl-controller

.PHONY: build clean run test kind-create-cluster kind-clean

build:
	go build -o $(BINARY) .

clean:
	-rm $(BINARY)

run: build
	ENVIRONMENT=dev ./$(BINARY)

test:
	go test ./... -cover

########
# Kind #
########

kind-create-cluster:
	kind create cluster --name k8s-ttl-controller

kind-clean: clean
	kind delete cluster --name k8s-ttl-controller
