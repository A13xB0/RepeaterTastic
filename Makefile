VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOFLAGS := -trimpath
DIST := dist

.PHONY: all build ui test race interop dist clean proto firmware

all: build

build:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/repeatertastic ./cmd/repeatertastic
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/kisstool ./cmd/kisstool

ui:
	cd ui && npm ci && npm run build

test:
	go vet ./...
	go test ./...

race:
	go test -race ./...

proto:
	./scripts/gen-proto.sh

# Static binaries for a Raspberry Pi (64-bit OS, 32-bit OS, Pi Zero/1) and x86-64.
dist:
	@mkdir -p $(DIST)
	@for t in linux/arm64 linux/arm/7 linux/arm/6 linux/amd64; do \
		os=$${t%%/*}; rest=$${t#*/}; arch=$${rest%%/*}; arm=$${rest#*/}; \
		suffix=$$arch; [ "$$arch" = arm ] && suffix=armv$$arm; \
		echo "building $$os-$$suffix"; \
		GOOS=$$os GOARCH=$$arch GOARM=$$( [ "$$arch" = arm ] && echo $$arm ) CGO_ENABLED=0 \
			go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST)/repeatertastic-$$os-$$suffix ./cmd/repeatertastic || exit 1; \
		GOOS=$$os GOARCH=$$arch GOARM=$$( [ "$$arch" = arm ] && echo $$arm ) CGO_ENABLED=0 \
			go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST)/kisstool-$$os-$$suffix ./cmd/kisstool || exit 1; \
	done
	@cd $(DIST) && sha256sum repeatertastic-* kisstool-* > SHA256SUMS

firmware:
	./firmware/build.sh

clean:
	rm -rf bin $(DIST)
