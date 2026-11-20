GO_VERSION := 1.25
BINARY := dns-updater
PKG := github.com/IUCLabs/dns-updater
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo 0.0.0)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: all build test vet fmt tidy lint rpm clean patch minor major

all: build

build:
	mkdir -p dist
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY) ./cmd/$(BINARY)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

tidy:
	go mod tidy

lint: vet
	@gofmt -l . | grep . && { echo "gofmt needed"; exit 1; } || echo "gofmt clean"

rpm:
	GOTOOLCHAIN=auto go run github.com/goreleaser/goreleaser/v2@v2.20.2 release --snapshot --clean --skip=sign

clean:
	rm -rf $(BINARY) dist *.rpm

# Version bump targets: tag vX.Y.Z and push; the mirrored tag triggers the GitHub release.
patch: ; @$(MAKE) bump PART=patch
minor: ; @$(MAKE) bump PART=minor
major: ; @$(MAKE) bump PART=major

bump:
	@cur=$$(git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//'); \
	[ -n "$$cur" ] || cur=0.0.0; \
	MA=$$(echo "$$cur" | cut -d. -f1); \
	MI=$$(echo "$$cur" | cut -d. -f2); \
	PA=$$(echo "$$cur" | cut -d. -f3); \
	case "$(PART)" in \
	  major) MA=$$((MA+1)); MI=0; PA=0;; \
	  minor) MI=$$((MI+1)); PA=0;; \
	  patch) PA=$$((PA+1));; \
	  *) echo "unknown PART=$(PART)"; exit 1;; \
	esac; \
	new="v$$MA.$$MI.$$PA"; \
	echo "tagging $$new"; \
	git tag -a "$$new" -m "release $$new"; \
	git push origin "$$new"
