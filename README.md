# dovm

Create and manage DigitalOcean droplets from a declarative spec file.

> **Status:** under active rewrite in Go. `validate`, `create`, `status`,
> `list` and `destroy` work; the terminal UI is coming next.

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
name: example                         # droplets are tagged dovm:example
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

`name` identifies the cluster. Every droplet dovm creates is tagged
`dovm:<name>`, which is how dovm recognizes droplets it owns.

## Usage

```sh
dovm validate examples/cluster.yaml
```

`validate` checks the spec, then confirms the region, sizes, image and SSH keys
exist in your account and that each size is offered in the chosen region. It
prints the droplets that would be created and an estimated cost. Use
`--offline` to skip the API checks.

### Creating droplets

```sh
dovm create examples/cluster.yaml --dry-run   # show the plan only
dovm create examples/cluster.yaml             # asks before creating
dovm create examples/cluster.yaml --yes --rollback-on-failure
```

`create` validates the spec, lists your droplets once, and decides per host:

| Found | Action |
|---|---|
| no droplet with that name | create it |
| one droplet with that name, tagged `dovm:<name>` | leave it as is (differences in size or region are reported, not changed) |
| a droplet with that name *without* the tag, or several tagged | conflict: nothing is created until you resolve it |

Missing droplets are created in batches of up to 10 per role, then dovm waits
until each is `active` with a public IP (`--wait-timeout`, default 10m). When
it finishes it prints a summary and writes inventory files for the ready hosts
to `--output-dir` (default `output/`):

- `vms.json`: hostname, role, id, public/private IPs, region, size, tags
- `vms_ip_host.txt`: `hostname<TAB>public-ip`
- `vm_ips.txt`: one public IP per line
- `droplets.json`: raw API objects

It exits non-zero if any host is not ready. Without `--yes`, it asks for
confirmation, and refuses when not run from a terminal.

**Failures and re-runs.** Because existing tagged droplets are left alone,
running `create` again after a failure, timeout or Ctrl-C only creates what is
still missing. With `--rollback-on-failure`, a failed or interrupted run
instead deletes every droplet *that run* created, never ones that existed
before it.

**Retries.** Read requests are retried with exponential backoff on rate
limiting (429) and server errors (5xx), honoring `Retry-After`. Create requests
are handled more carefully: a 5xx can mean the droplets were created anyway, so
dovm checks which ones now exist and only retries the rest. If that check
itself fails, it stops rather than risk creating duplicates.

### Checking a cluster

```sh
dovm status examples/cluster.yaml          # spec vs. reality, one row per host
dovm list                                  # every dovm-managed droplet, all clusters
dovm list --cluster example -o json
```

`status` shows each host as `exists` (with droplet status, IPs and any size or
region drift), `missing` or `conflict`, and lists **orphans**: droplets tagged
for the cluster whose host has been removed from the spec. Both commands take
`-o json` for scripting.

### Destroying droplets

```sh
dovm destroy examples/cluster.yaml --dry-run          # show what would be deleted
dovm destroy examples/cluster.yaml                    # delete the spec's hosts
dovm destroy examples/cluster.yaml --host vm02        # delete just one host
dovm destroy examples/cluster.yaml --include-orphans  # also delete orphans
dovm destroy --name example                           # no spec: every droplet tagged dovm:example
```

Only droplets tagged `dovm:<name>` are ever deleted, so a droplet you made by
hand with the same name as a host is safe. You are asked to type the cluster
name to confirm; `--yes` skips this, and without a terminal destroy refuses
unless `--yes` is given. Inventory files in `output/` are not changed by
destroy.

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
| `internal/engine` | plan (compare spec with existing droplets), create, wait, rollback, destroy |
| `internal/output` | inventory files |
