set shell := ["bash", "-euo", "pipefail", "-c"]
set positional-arguments
set dotenv-load

# Run all project checks.
check: tidy typos fmt lint vet test test-progress test-flake test-e2e fuzz

# Update the Go module files.
tidy:
    go mod tidy

# Find spelling errors.
typos:
    typos

# Format all tracked source files.
fmt: markdown-fmt
    golangci-lint fmt ./...
    git ls-files -z -- '*.go' | xargs -0 -r gofmt -w

# Format all tracked Markdown files.
markdown-fmt:
    git ls-files -z -- '*.md' | xargs -0 -r mdformat --wrap 80

# Run the Go linters.
lint:
    golangci-lint run ./...

# Find suspicious Go constructs.
vet:
    go vet ./...

# Run all tests with the race detector and coverage measurement.
test:
    go test -race -cover ./...

# Validate command progress packets with zpg (available in nix develop).
test-progress:
    GHREPO_REQUIRE_ZPG=1 go test -race -count=1 -v ./cmd -run '^TestZPGFlows$'

# Show real offline package work in zpg's live progress tree.
demo-progress:
    @command -v zpg >/dev/null || { printf 'zpg is required; run nix develop first\n' >&2; exit 1; }
    @demo_dir="$(mktemp -d)"; trap 'rm -rf "$demo_dir"' EXIT; \
      go test -c -o "$demo_dir/ghrepo-progress-demo" ./cmd; \
      (cd cmd && GHREPO_PROGRESS_DEMO=1 zpg \
        --name 'ghrepo progress demo' "$demo_dir/ghrepo-progress-demo" \
        -test.run '^TestProgressDemo$' -test.v)

# Build the installable Nix package and check its dependency hash.
test-flake:
    nix build .#ghrepo --no-link

# Check generated repositories with APT, DNF, and RPM in Docker.
test-e2e:
    bash tests/e2e/docker/run.sh

# Run the disposable live GitHub Pages test (requires GPG approval).
test-github:
    bash tests/e2e/github/run.sh

# Run each Go fuzz target. The duration applies to each target.
fuzz fuzztime=env_var_or_default('FUZZ_TIME', '5s') fuzzparallel=env_var_or_default('FUZZ_PARALLEL', '2'):
    @found=0; \
    while IFS=: read -r file _ target; do \
      found=1; \
      package="./$(dirname "$file")"; \
      printf 'Fuzzing %s in %s for %s\n' "$target" "$package" {{ quote(fuzztime) }}; \
      go test "$package" -run '^$' -fuzz "^${target}$" -fuzztime {{ quote(fuzztime) }} -parallel {{ quote(fuzzparallel) }}; \
    done < <(rg --line-number --no-heading --replace '$1' '^func (Fuzz[[:alnum:]_]+)\(.*$' --glob '*_test.go' 2>/dev/null); \
    if ((found == 0)); then printf 'No Go fuzz targets found.\n'; fi

# Build the ghrepo executable.
build:
    go build -o ghrepo .

# Run checks, create and push a version tag, then publish signed assets.
release version:
    bash scripts/release.sh {{ quote(version) }}

# Run ghrepo with the supplied CLI arguments.
run *args:
    go run . "$@"

# Show the available recipes.
default:
    @just --list
