.PHONY: build
build:
	go build ./cmd/ec2-ssh-proxy

.PHONY: fmt
fmt:
	go fmt ./cmd/ec2-ssh-proxy
