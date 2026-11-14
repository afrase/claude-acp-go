# golangci-lint and govulncheck, pinned in their own module so this
# module's go.mod lists only what the code needs.
TOOL := go tool -modfile=tools/lint/go.mod

.PHONY: build vet test lint vuln check

build:
	go build ./...

vet:
	go vet ./...

# A new order each run: a test that passes only after another shows
# (the output has the seed).
test:
	go test -race -shuffle=on ./...

lint:
	$(TOOL) golangci-lint run ./...

vuln:
	$(TOOL) govulncheck ./...

check: build vet lint vuln test
