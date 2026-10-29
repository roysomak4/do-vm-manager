# dovm

Create and manage DigitalOcean droplets from a declarative spec file.

> **Status:** under active rewrite in Go. Phase 1 (API client and spec
> validation) is done; droplet creation, status, destroy and the terminal UI
> are coming in later phases.

## Install

Requires Go 1.27+.

```sh
go install github.com/roysomak4/do-vm-manager/cmd/dovm@latest
# or, from a checkout
go build -o dovm ./cmd/dovm
```

## Authentication

dovm talks to the DigitalOcean API directly; `doctl` is not required. The API
token is taken from the first of:

1. `--token` flag
2. `DIGITALOCEAN_TOKEN` or `DIGITALOCEAN_ACCESS_TOKEN` environment variable
3. doctl's config file for its current auth context, if you have run `doctl auth init`

## Cluster spec

Droplets are declared in a YAML file. Each role has its own size and list of
hostnames, and every droplet is tagged with its role name plus any tags you
list. See [`examples/cluster.yaml`](examples/cluster.yaml):

```yaml
region: nyc3
image: ubuntu-24-04-x64
ssh_keys: [3871344, "3b:16:bf:..."]   # key IDs or fingerprints
tags: [example]
monitoring: true
roles:
  manager:
    size: s-2vcpu-4gb
    hosts: [cp01, cp02, cp03]
  worker:
    size: s-4vcpu-8gb
    hosts: [vm01, vm02]
```

Unknown fields are rejected, so typos fail loudly.

## Usage

```sh
dovm validate examples/cluster.yaml
```

`validate` checks the spec, then confirms the region, sizes, image and SSH keys
exist in your account and that each size is offered in the chosen region. It
prints the droplets that would be created and an estimated cost. Use
`--offline` to skip the API checks.

API requests are retried with exponential backoff on rate limiting (429) and
server errors (5xx), honoring `Retry-After`.

## Development

```sh
go test -race ./...
go vet ./...
```

Layout:

| Path | Purpose |
|---|---|
| `cmd/dovm` | entry point |
| `internal/cli` | command definitions |
| `internal/cloud` | DigitalOcean API client: auth, retries, pagination |
| `internal/spec` | spec parsing, static validation, live checks against the account |
