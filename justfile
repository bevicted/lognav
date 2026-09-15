bench_count := "10"
cov_baseline := "70"
set positional-arguments

# --- Umbrellas (start here) ---

# check is the local-dev one-shot: format, build, lint, test. Fast iteration
# loop; the fuller pre-push pass (vuln, race, fuzz, coverage) lives in `just ci`.
check:
    just fmt && just build && just lint && just test

ci:
    just build && just fmt-check && just lint && just vuln && just test-race && just fuzz && just cov-check && just bin-size

# --- Run & build ---

# `go run` can't stamp VCS info, so build (which can) to a temp path and exec it
# — `lognav version` then shows the real commit hash, matching released builds.
run *args:
    #!/bin/bash
    go build -trimpath -o "${TMPDIR:-/tmp}/lognav-dev" .
    exec "${TMPDIR:-/tmp}/lognav-dev" "$@"

build:
    go build -trimpath -o lognav .

install-remote:
    go install -trimpath github.com/bevicted/lognav@latest

# --- Format & lint ---

fmt:
    go fmt ./...
    golangci-lint fmt ./...
    go mod tidy
    bunx prettier --write "**/*.md"

fmt-check:
    golangci-lint fmt --diff ./...
    go mod tidy -diff
    bunx prettier --check "**/*.md"

lint:
    golangci-lint run ./...

lint-fix:
    golangci-lint run --fix ./...

vuln:
    govulncheck ./...

# --- Tests & coverage ---

test:
    go test -coverprofile=coverage.out ./...

test-race:
    go test -race -count=1 ./...

fuzz:
    go test -run='^$' -fuzz=. -fuzztime=10s ./internal/snapshot/

cov:
    go tool cover -html=coverage.out

# cov-check fails only if total coverage drops more than 1% below the
# cov_baseline variable above (increases are always fine).
cov-check:
    #!/usr/bin/env bash
    set -euo pipefail
    baseline={{cov_baseline}}
    go test -coverprofile=coverage.out ./... >/dev/null
    total=$(go tool cover -func=coverage.out | awk '/^total:/{print $3}' | tr -d '%')
    echo "cov-check: total ${total}% (baseline ${baseline}%, floor = baseline - 1%)"
    awk -v t="$total" -v b="$baseline" 'BEGIN{ if (t < b - 1.0) exit 1 }' || {
        echo "cov-check: coverage dropped more than 1% below baseline" >&2
        exit 1
    }

# --- Benchmarks ---

# Benchmark LogStore memory and compute at 1M rows.
bench-logstore:
    go test ./internal/ui/components/logviewer/ -run '^$' -bench 'BenchmarkStore_' -benchmem -benchtime 1x

bench:
    go test -bench=. -benchmem -run=^$ -count={{bench_count}} -p 1 ./...

bench-check:
    @command -v benchstat >/dev/null 2>&1 || go install golang.org/x/perf/cmd/benchstat
    tmp="$(mktemp)"; \
        trap 'rm -f "$tmp"' EXIT; \
        if ! go test -bench=. -benchmem -run=^$ -count={{bench_count}} -p 1 ./... > "$tmp"; then \
            echo "bench-check: 'go test' failed; benchstat comparison skipped" >&2; \
            exit 1; \
        fi; \
        benchstat bench/baseline.txt "$tmp"

update-baseline:
    @mkdir -p bench
    @echo "# benchmark baseline; regenerate via 'just update-baseline'" > bench/baseline.txt
    @just bench >> bench/baseline.txt

# --- Profiling & race ---

cpuprof:
    LOGNAV_CPUPROF="true" just run
    # open graph with
    # go tool pprof -http=:9090 ./cpu.pprof

memprof:
    LOGNAV_MEMPROF="true" just run
    # open graph with
    # go tool pprof -http=:9090 ./mem.pprof

trace:
    LOGNAV_TRACE="true" just run
    # open trace with
    # go tool trace trace.out

race:
    (go run -race ./main.go 2> race.txt && rm race.txt) || (cat race.txt && exit 1)

# bin-size is informational (never fails) — emits the current binary size.
bin-size:
    #!/usr/bin/env bash
    set -euo pipefail
    out=$(mktemp)
    go build -trimpath -o "$out" .
    printf 'bin-size: %s bytes\n' "$(wc -c < "$out" | tr -d ' ')"
    rm -f "$out"
