PROJECT = sciuro
MODULE = github.com/cloudflare/$(PROJECT)

.PHONY: default
default: build;

.PHONY: clean
clean:
	@echo cleaning build targets
	@rm -rf bin coverage.txt

.PHONY: clean-bazel
clean-bazel:
	@echo cleaning bazel build targets
	@./tools/bazel clean

.PHONY: check
check:
	@echo running checks
	@./tools/bazel run //:golangcilint

.PHONY: dep-fix
dep-fix:
	@echo fixing dependencies
	@./tools/bazel run //:gazelle -- fix

.PHONY: dep-update
dep-update: go.sum
	@echo updating dependencies
	@go mod tidy
	@./tools/bazel run //:gazelle -- update-repos -from_file=go.mod -prune=true -to_macro=gazelle.bzl%deps

.PHONY: test
test:
	@echo unit testing with Bazel
	@./tools/bazel test //...

.PHONY: test-coverage
test-coverage:
	@echo unit testing with coverage
	@go test -coverprofile=coverage.txt -mod=readonly -covermode=atomic $(MODULE)/...

.PHONY: build
build:
	@echo building cmds and images
	@./tools/bazel build //cmd/...
