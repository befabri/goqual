# goqual

Go quality tooling in one command.

`goqual` combines:

- CRAP reports: cyclomatic complexity plus test coverage.
- Mutation testing: covered mutation sites are changed one at a time and tested.

Mutation manifests are stored as sidecar JSON files under `target/goqual/` by
default. Source files are restored without embedded mutation comments after each
run.

## Setup

Install while developing locally:

```sh
go install ./cmd/goqual
```

After publishing the repo, pin it per project with Go's tool support:

```sh
go get -tool github.com/befabri/goqual/cmd/goqual@latest
go tool goqual --help
```

## Usage

Run a CRAP report:

```sh
goqual crap
goqual crap internal/platform
goqual crap --test-command "go test ./internal/app ./internal/input"
goqual crap --reuse-coverage
```

Scan a file for mutation sites:

```sh
goqual mutate internal/app/app.go --scan
```

Create or refresh the sidecar manifest without running tests:

```sh
goqual mutate internal/app/app.go --update-manifest
```

Run mutation testing:

```sh
goqual mutate internal/app/app.go --max-workers 3
```

Fail the command when mutation testing finds survivors or uncovered mutations:

```sh
goqual mutate internal/app/app.go --strict
goqual mutate internal/app/app.go --fail-on-survived
goqual mutate internal/app/app.go --fail-on-uncovered
```

Commands can be run from a module subdirectory. Source files can be passed as
current-directory-relative paths like `foo.go` or module-root-relative paths
like `internal/app/app.go`. `goqual` runs coverage, tests, and sidecar manifest
writes from the module root.

Use custom storage paths:

```sh
goqual mutate internal/app/app.go \
  --coverage-profile target/coverage/coverage.out \
  --manifest-dir target/goqual/manifests
```

## Mutation Options

```text
--scan
--update-manifest
--reuse-coverage
--lines L1,L2,...
--since-last-run
--mutate-all
--strict
--fail-on-survived
--fail-on-uncovered
--mutation-warning N
--timeout-factor N
--test-command COMMAND
--coverage-profile PATH
--manifest-dir DIR
--max-workers N
--verbose
```

## Development

```sh
go test ./...
go run ./cmd/goqual --help
go run ./cmd/goqual crap --reuse-coverage
go run ./cmd/goqual mutate internal/crap/crap.go --scan
```

## License

Copyright (c) 2026 Benjamin Fabri. All rights reserved.

Portions are adapted from `crap4go` and `mutate4go`, which are Copyright (c)
Robert C. Martin. All rights reserved.
