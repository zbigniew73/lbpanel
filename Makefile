.PHONY: build build-panel build-agent clean run tidy

# Build both binaries
build: build-panel build-agent

build-panel:
	CGO_ENABLED=1 go build -ldflags="-s -w" -o lbpanel ./cmd/lbpanel/

build-agent:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o lbagent ./cmd/lbagent/

# Run panel locally (dev)
run:
	CGO_ENABLED=1 go build -o lbpanel ./cmd/lbpanel/ && \
	./lbpanel --db ./dev.db --addr 0.0.0.0:4040 --certs ./dev-certs

# Cross-compile for Linux amd64 (from Windows/Mac)
build-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=1 \
	CC=x86_64-linux-musl-gcc \
	go build -ldflags="-s -w" -o lbpanel-linux ./cmd/lbpanel/

	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
	go build -ldflags="-s -w" -o lbagent-linux ./cmd/lbagent/

tidy:
	go mod tidy

clean:
	rm -f lbpanel lbagent lbpanel-linux lbagent-linux dev.db
	rm -rf dev-certs/
