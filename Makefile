VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
FUZZTIME  ?= 20s
IMAGE     ?= fugace:local
LDFLAGS   := -s -w -buildid= -X main.version=$(VERSION)
# Pinned: never run an unversioned tool from the network.
GOVULNCHECK_VERSION ?= v1.8.0

.PHONY: build test lint vuln fuzz check docker smoke clients run clean

build:
	CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="$(LDFLAGS)" -o bin/fugace ./cmd/fugace

test:
	go test -race -count=1 -coverprofile=coverage.out ./...

lint:
	golangci-lint run ./...
	@! grep -rnE 'innerHTML|outerHTML|insertAdjacentHTML|document\.write|eval\(' web/static \
		|| (echo "forbidden DOM sink in web/static" && exit 1)

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

# Runs every FuzzXxx target found, FUZZTIME each.
fuzz:
	@for pkg in $$(go list ./...); do \
		for f in $$(go test -list '^Fuzz' $$pkg | grep '^Fuzz'); do \
			echo "== $$pkg $$f"; \
			go test -run='^$$' -fuzz="^$$f$$" -fuzztime=$(FUZZTIME) $$pkg || exit 1; \
		done; \
	done

check: lint test vuln

docker:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

smoke: docker
	./scripts/smoke.sh $(IMAGE)

# Real WeeChat and irssi clients in containers (needs docker, nc, python3).
clients: docker
	./scripts/clients/run.sh $(IMAGE)

run: build
	./bin/fugace

clean:
	rm -rf bin dist coverage.out
