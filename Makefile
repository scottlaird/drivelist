# Build and package drivelist.
#
#   make            build ./drivelist for this machine
#   make test       go vet + go test -race
#   make deb        dist/drivelist_<version>_{amd64,arm64,armhf}.deb (needs nfpm:
#                   go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest)
#   make deb-arm64  one architecture: amd64, arm64 (64-bit Raspberry Pi OS),
#                   armhf (32-bit Raspberry Pi OS, ARMv7)
#
# VERSION: on a tagged commit, the tag (v1.2.3 -> 1.2.3). Past a tag,
# 1.2.3+git<date>.<sha>, which Debian sorts after 1.2.3 and before 1.2.4.
# With no tag at all, 0.0.0~git<date>.<sha>, before any real release.

MODULE  := github.com/scottlaird/drivelist
EXACT   := $(shell git describe --tags --exact-match 2>/dev/null)
TAG     := $(shell git describe --tags --abbrev=0 2>/dev/null)
SHA     := $(shell git rev-parse --short HEAD 2>/dev/null)
SNAP    := git$(shell date -u +%Y%m%d).$(SHA)
ifneq ($(EXACT),)
VERSION ?= $(patsubst v%,%,$(EXACT))
else ifneq ($(TAG),)
VERSION ?= $(patsubst v%,%,$(TAG))+$(SNAP)
else
VERSION ?= 0.0.0~$(SNAP)
endif
LDFLAGS := -X $(MODULE)/internal/report.Version=$(VERSION)

# Debian architecture -> GOARCH (and GOARM for 32-bit ARM); nfpm takes the
# Go-style name in its arch field and maps arm7 to armhf itself.
ARCHES := amd64 arm64 armhf
GOARCH_amd64 := amd64
GOARCH_arm64 := arm64
GOARCH_armhf := arm
GOARM_armhf  := 7
NFPM_amd64   := amd64
NFPM_arm64   := arm64
NFPM_armhf   := arm7

.PHONY: all build test deb clean FORCE
.SECONDARY:   # keep the cross-built binaries the pattern rule makes

all: build

build:
	go build -ldflags '$(LDFLAGS)' ./cmd/drivelist

test:
	go vet ./...
	go test -race ./...

# FORCE makes the binaries rebuild every time: the version baked in by
# LDFLAGS changes with tags, not with sources, and a stale binary in dist/
# would otherwise be packaged under a new version. go build is incremental,
# so this costs little.
dist/drivelist-linux-%: FORCE
	mkdir -p dist
	GOOS=linux GOARCH=$(GOARCH_$*) GOARM=$(GOARM_$*) CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o $@ ./cmd/drivelist

FORCE:

deb-%: dist/drivelist-linux-%
	cp $< dist/drivelist-bin
	VERSION='$(VERSION)' ARCH='$(NFPM_$*)' nfpm package --config nfpm.yaml --packager deb --target dist/
	rm -f dist/drivelist-bin

deb: $(addprefix deb-,$(ARCHES))
	@ls -1 dist/*.deb

clean:
	rm -rf dist drivelist
