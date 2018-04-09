# Disable make's implicit rules, which are not useful for golang, and slow down the build
# considerably.
.SUFFIXES:


GO_PATH=$(GOPATH)
SRCFILES=main.go

# Ensure that the dist directory is always created
MAKE_SURE_DIST_EXIST := $(shell mkdir -p dist)

.PHONY: all plugin
default: clean all
all: plugin
plugin: dist/genie-policy

.PHONY: clean
clean:
	rm -rf dist

# Build the genie cni plugin
dist/genie-policy: $(SRCFILES)
	@GOPATH=$(GO_PATH) CGO_ENABLED=0 go build -v -i -o dist/genie-policy \
	-ldflags "-X main.VERSION=1.0 -s -w" main.go networkpolicycontroller.go
