.PHONY: all build test clean e2e package

all: build test

build:
	./build.sh

test:
	cd services/dc-fs-engine && cargo test
	cd services/dc-gateway && go test -v ./...
	cd services/dc-doc-service && go test -v ./...

e2e: build
	./tests/e2e_test.sh

package:
	./scripts/package.sh

clean:
	rm -rf bin/a2m* bin/dc-* bin/desktop-commander dist apps/desktop/macos/A2M.app
	cd services/dc-fs-engine && cargo clean
