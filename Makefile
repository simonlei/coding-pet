.PHONY: build build-agent build-server clean

build: build-agent build-server

build-agent:
	go build -o dashboard-agent ./cmd/agent/

build-server:
	go build -o dashboard-server ./cmd/server/

clean:
	rm -f dashboard-agent dashboard-server
