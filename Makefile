.PHONY: build build-agent build-server clean

build: build-agent build-server

build-agent:
	go build -o coding-pet-agent ./cmd/agent/

build-server:
	go build -o coding-pet-server ./cmd/server/

clean:
	rm -f coding-pet-agent coding-pet-server
