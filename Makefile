# Build and package drivelist.
#
#   make            build ./drivelist for this machine
#   make test       go vet + go test -race
#   make deb        dist/drivelist_<version>_amd64.deb for Linux (needs nfpm:
#                   go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest)
#
# VERSION comes from the newest tag (v1.2.3 -> 1.2.3); with no tag it is
# 0.0.0~git<date>.<sha>, which Debian sorts before any real release.

MODULE  := github.com/scottlaird/drivelist
TAG     := $(shell git describe --tags --abbrev=0 2>/dev/null)
SHA     := $(shell git rev-parse --short HEAD 2>/dev/null)
ifeq ($(TAG),)
VERSION ?= 0.0.0~git$(shell date -u +%Y%m%d).$(SHA)
else
VERSION ?= $(patsubst v%,%,$(TAG))
endif
LDFLAGS := -X $(MODULE)/internal/report.Version=$(VERSION)

.PHONY: all build test deb clean

all: build

build:
	go build -ldflags '$(LDFLAGS)' ./cmd/drivelist

test:
	go vet ./...
	go test -race ./...

dist/drivelist-linux-amd64:
	mkdir -p dist
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o $@ ./cmd/drivelist

deb: dist/drivelist-linux-amd64
	VERSION='$(VERSION)' nfpm package --config nfpm.yaml --packager deb --target dist/
	@ls -1 dist/*.deb

clean:
	rm -rf dist drivelist
