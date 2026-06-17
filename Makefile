BINARY := cpanel-self-migration
PKG    := github.com/tis24dev/cPanel_self-migration

.PHONY: build test test-race fmt vet tidy clean

build:
	go build -o $(BINARY) ./cmd/cpanel-self-migration

# Unit + golden + in-process integration tests (no network, no live cPanel). The
# integration tests run real bash/tar/find/mysql locally against an in-process SSH
# server and skip automatically where those tools are absent.
test:
	go test ./...

# The same suite under the race detector (what CI's race workflow runs).
test-race:
	go test -race -count=1 ./...

fmt:
	gofmt -w .

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -f $(BINARY)
