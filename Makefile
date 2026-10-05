GO                 = go
GOFMT              = gofmt
# Pinned, like golangci-lint in CI. go run leaves go.mod alone.
BENCHSTAT          = $(GO) run golang.org/x/perf/cmd/benchstat@v0.0.0-20260908200009-22c9c6c9d4da

# Which benchmarks run (a regexp), how many times benchcmp runs each tree, and what it compares the working tree with.
BENCH             ?= .
BENCH_COUNT       ?= 10
BENCH_BASE        ?= main
BENCH_DIR          = .bench
BENCH_FLAGS        = -run '^$$' -bench '$(BENCH)' -benchmem

# How many times stress runs TestStress.
STRESS_COUNT      ?= 50

# The modules tidy, lint and test cover. An example with dependencies of its own is a module of its own, so that the
# library's go.mod lists only uuid.
MODULES            = . examples/19-redis

# For CI
ifneq ($(wildcard ./bin/golangci-lint),)
	GOLINT = $(CURDIR)/bin/golangci-lint
else
	GOLINT = golangci-lint
endif

V                 = 0
Q                 = $(if $(filter 1,$V),,@)
M                 = $(shell printf "\033[0;35m▶\033[0m")

.PHONY: all
all: check

# Tidy
.PHONY: tidy
tidy: ## Run go mod tidy
	$(info $(M) running go mod tidy) @
	$Q for m in $(MODULES); do (cd $$m && $(GO) mod tidy) || exit 1; done

# Check
.PHONY: check
check: lint test ## Run lint and tests

# Lint
.PHONY: lint
lint: ## Run golangci-lint
	$(info $(M) running $(GOLINT))
	$Q for m in $(MODULES); do (cd $$m && $(GOLINT) run) || exit 1; done

# Test
.PHONY: test
# GOMAXPROCS 1 interleaves goroutines differently from several cores, and -shuffle, whose seed -v prints, catches tests
# that depend on each other. Each benchmark runs once too, since go test alone compiles them but never runs them.
test: ## Run tests with race detector, shuffled, with GOMAXPROCS 1 and 4, and each benchmark once
	$(info $(M) running go test) @
	$Q for m in $(MODULES); do (cd $$m && $(GO) test -cover -race -shuffle=on -cpu 1,4 -v -bench . -benchtime 1x ./...) || exit 1; done

# Stress
.PHONY: stress
stress: ## Run TestStress STRESS_COUNT (50) times with race detector
	$(info $(M) running TestStress $(STRESS_COUNT) times) @
	$Q $(GO) test -race -run '^TestStress$$' -count $(STRESS_COUNT) .

# Bench
.PHONY: bench
bench: ## Run benchmarks (BENCH=regexp)
	$(info $(M) running go test -bench) @
	$Q $(GO) test $(BENCH_FLAGS) ./...

# The runs alternate between the two trees, so that drift, such as a laptop heating up, affects both alike. The results
# stay in $(BENCH_DIR), for benchstat with other flags.
.PHONY: benchcmp
benchcmp: ## Compare benchmarks of BENCH_BASE (main) and the working tree with benchstat
	$(info $(M) running benchmarks of $(BENCH_BASE) and the working tree, $(BENCH_COUNT) times each) @
	$Q set -e; \
	base=$$(mktemp -d); trap 'rm -rf "$$base"' EXIT; \
	git archive -o "$$base/src.tar" $(BENCH_BASE); tar -x -f "$$base/src.tar" -C "$$base"; \
	mkdir -p $(BENCH_DIR); rm -f $(BENCH_DIR)/base.txt $(BENCH_DIR)/worktree.txt; \
	for i in $$(seq $(BENCH_COUNT)); do \
		echo "$(M) run $$i of $(BENCH_COUNT)"; \
		(cd "$$base" && $(GO) test $(BENCH_FLAGS) -count 1 ./...) >> $(BENCH_DIR)/base.txt; \
		$(GO) test $(BENCH_FLAGS) -count 1 ./... >> $(BENCH_DIR)/worktree.txt; \
	done
	$Q $(BENCHSTAT) $(BENCH_BASE)=$(BENCH_DIR)/base.txt worktree=$(BENCH_DIR)/worktree.txt

## Helpers

.PHONY: go-version
go-version: ## Print go version used in this makefile
	$Q echo $(GO)

.PHONY: fmt
fmt: ## Format code
	$(info $(M) running $(GOFMT)) @
	$Q $(GOFMT) -l -w .

.PHONY: doc
doc: ## Print package documentation
	$(info $(M) running go doc) @
	$Q $(GO) doc -all .

.PHONY: help
help: ## Print this
	@grep -E '^[ a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'
