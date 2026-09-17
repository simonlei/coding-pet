.PHONY: build build-agent build-server build-watch clean

build: build-agent build-server

build-agent:
	go build -o coding-pet-agent ./cmd/agent/

build-server:
	go build -o coding-pet-server ./cmd/server/

# 调试工具：只读监控本机 agent 状态变化，不参与发布
build-watch:
	go build -o coding-pet-watch ./cmd/watch/

clean:
	rm -f coding-pet-agent coding-pet-server coding-pet-watch
