BINARY     = statusline
DIST       = dist
INSTALL    = $(HOME)/.claude/statusline/$(BINARY)

.PHONY: demo local build tag release clean

# Run the demo in-place (no install)
demo:
	go run . --demo

# Build and install locally, backing up the current binary first
local:
	@cp $(INSTALL) /tmp/$(BINARY)-backup 2>/dev/null && echo "Backed up to /tmp/$(BINARY)-backup" || true
	go build -o $(INSTALL) .
	@echo "Installed → $(INSTALL)"

# Cross-compile release binaries (requires: make build VERSION=v1.x.x)
build:
	@test -n "$(VERSION)" || (echo "Usage: make build VERSION=v1.x.x" && exit 1)
	@mkdir -p $(DIST)
	GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o $(DIST)/$(BINARY)-darwin-arm64 .
	GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o $(DIST)/$(BINARY)-darwin-amd64 .
	@echo "Built $(DIST)/$(BINARY)-darwin-{arm64,amd64}"

# Create and push an annotated tag (requires: make tag VERSION=v1.x.x)
tag:
	@test -n "$(VERSION)" || (echo "Usage: make tag VERSION=v1.x.x" && exit 1)
	git tag -a $(VERSION) -m "$(VERSION)"
	git push origin $(VERSION)

# Build + create GitHub release (requires: make release VERSION=v1.x.x)
release: build
	gh release create $(VERSION) \
		$(DIST)/$(BINARY)-darwin-arm64 \
		$(DIST)/$(BINARY)-darwin-amd64 \
		--title "$(VERSION)" \
		--generate-notes

clean:
	rm -rf $(DIST)
