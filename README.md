# dovm

Create and manage DigitalOcean droplets from a declarative spec file.

You describe a cluster (droplets grouped into roles, each role with a size and
a list of hostnames) in a YAML file. `dovm create` makes whatever is missing,
waits until every droplet is up, and writes inventory files: JSON, plain IP
lists and an Ansible inventory. Running it again is safe; it only creates
what is still missing. `dovm destroy` deletes only droplets dovm created.

## Install

Requires Go 1.27+.

```sh
go install github.com/roysomak4/do-vm-manager/cmd/dovm@latest
# or, from a checkout
go build -o dovm ./cmd/dovm
```

Release builds for Linux, macOS and Windows are attached to
[GitHub releases](https://github.com/roysomak4/do-vm-manager/releases) once a
version is tagged.

## Try it on your account

This walkthrough creates three of the smallest droplets (about $0.03/hour in
total), exercises every command, and deletes them again. Everything up to
step 4 is free and read-only. Run it from the repository root.

**1. Build and check access.**

```sh
go build -o dovm ./cmd/dovm
./dovm --version
./dovm list            # read-only; prints "No droplets managed by dovm."
```

If `list` reports a missing token, run `doctl auth init`, or
`export DIGITALOCEAN_TOKEN=...` with a token from
*API → Tokens* in the DigitalOcean control panel (it needs read and write
scope). See [Authentication](#authentication).

**2. Write a test spec.** Copy the small example into `hosts/`, which git
ignores, and add your SSH key so you can log in:

```sh
mkdir -p hosts
cp examples/smoke-test.yaml hosts/test.yaml
doctl compute ssh-key list      # or see Settings → Security in the control panel
# edit hosts/test.yaml: ssh_keys: [<your key ID>]
```

Or build one interactively with the wizard, which lists the regions, images,
sizes and SSH keys available to your account:

```sh
./dovm init hosts/test.yaml
```

**3. Check it (free).**

```sh
./dovm validate hosts/test.yaml               # checks against your account, shows cost
./dovm create hosts/test.yaml --dry-run       # shows what would be created
./dovm status hosts/test.yaml                 # every host "missing" for now
```

**4. Create the droplets (costs money from here on).**

```sh
./dovm create hosts/test.yaml
```

Answer `y`. The dashboard shows each droplet going from *requesting* to
*provisioning* to *ready*, which usually takes one to two minutes. Then:

```sh
ls output/                      # vms.json, vms_ip_host.txt, vm_ips.txt, inventory.ini, droplets.json
cat output/vms_ip_host.txt
ssh root@<ip from that file>    # if you added an SSH key
./dovm status hosts/test.yaml   # all three "exists", status active
./dovm list
```

**5. Re-run and partial changes.**

```sh
./dovm create hosts/test.yaml                     # "0 to create, 3 already exist"; nothing new is made
./dovm destroy hosts/test.yaml --host test-vm02   # type "dovm-test" to confirm
./dovm status hosts/test.yaml                     # test-vm02 is now "missing"
./dovm create hosts/test.yaml                     # recreates only test-vm02
```

To see orphan handling, delete `test-vm02` from the spec file and run
`./dovm status hosts/test.yaml`: the droplet is listed as an orphan.
`./dovm destroy hosts/test.yaml --include-orphans` would delete it too. Put
it back in the spec before continuing.

**6. Interrupting a run.** Start from nothing so there is something to
interrupt:

```sh
./dovm destroy hosts/test.yaml
./dovm create hosts/test.yaml --yes --rollback-on-failure
```

Press Ctrl-C while rows show *provisioning*. The dashboard stays up while it
deletes the droplets this run created, then exits with an error. `./dovm list`
shows nothing left. Now try it without rollback:

```sh
./dovm create hosts/test.yaml --yes     # Ctrl-C again while provisioning
./dovm status hosts/test.yaml           # droplets already created were kept
./dovm create hosts/test.yaml --yes     # finishes the job
```

**7. Plain output.** The dashboard turns off automatically when output is not
a terminal, or with `--no-tui`:

```sh
./dovm status hosts/test.yaml -o json
./dovm create hosts/test.yaml --yes --no-tui    # timestamped log lines
```

**8. Clean up.**

```sh
./dovm destroy hosts/test.yaml          # type "dovm-test" to confirm
./dovm list                             # should print "No droplets managed by dovm."
```

If anything goes wrong mid-test, `./dovm destroy --name dovm-test` deletes
every droplet tagged `dovm:dovm-test` without needing the spec, and the
*Droplets* page of the control panel shows what is left. Droplets you created
by hand are never touched: dovm only deletes droplets carrying its tag.

## Authentication

dovm talks to the DigitalOcean API directly; `doctl` is not required. The API
token is taken from the first of:

1. `--token` flag
2. `DIGITALOCEAN_TOKEN` or `DIGITALOCEAN_ACCESS_TOKEN` environment variable
3. doctl's config file for its current auth context, if you have run `doctl auth init`

## Cluster spec

There are three ways to get a spec:

- `dovm init [FILE]`: an interactive wizard (see below).
- `dovm import HOSTFILE`: convert a host list from the old Python script (see
  [Migrating from create_vm.py](#migrating-from-create_vmpy)).
- Write it by hand, starting from [`examples/cluster.yaml`](examples/cluster.yaml).

Each role has its own size and list of hostnames, and every droplet is tagged
with its role name plus any tags you list:

```yaml
name: example                         # droplets are tagged dovm:example
region: nyc3
image: ubuntu-24-04-x64
ssh_keys: [3871344, "3b:16:bf:..."]   # key IDs or fingerprints
tags: [example]
monitoring: true
ipv6: false                           # optional
# vpc_uuid: ...                       # optional; default VPC of the region otherwise
roles:
  manager:
    size: s-2vcpu-4gb
    hosts: [cp01, cp02, cp03]
  worker:
    size: s-4vcpu-8gb
    hosts: [vm01, vm02]
    tags: [pool-a]                    # optional extra tags for this role
```

Unknown fields are rejected, so typos fail loudly.

`name` identifies the cluster. Every droplet dovm creates is tagged
`dovm:<name>`, which is how dovm recognizes droplets it owns. There is no local
state file: the tag is the source of truth.

### The wizard

```sh
dovm init cluster.yaml
```

The wizard offers the regions, images (newest first), sizes (cheapest first,
with prices) and SSH keys available to your account. Hostnames can be listed
(`web01, web02`) or generated (`worker*3` gives `worker01`, `worker02`,
`worker03`). Before saving it checks the spec against your account and shows
the monthly cost. It needs a terminal and will not overwrite an existing file
without `--force`.

## Usage

### Validating

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
- `inventory.ini`: Ansible inventory with one group per role, `ansible_host`
  set to the public IP and `ansible_user=root`
- `droplets.json`: raw API objects

The first three keep the format of the old Python script. Running `create`
again with nothing to create refreshes these files.

It exits non-zero if any host is not ready. Without `--yes`, it asks for
confirmation, and refuses when not run from a terminal.

In a terminal, `create` and `destroy` show a live dashboard: one row per
droplet with its state, droplet ID, IP and elapsed time, an overall progress
bar, and the most recent events. Ctrl-C cancels (with
`--rollback-on-failure`, that rolls back) and the dashboard stays up until
cleanup finishes. Pass `--no-tui` for plain timestamped log lines instead;
this is automatic when output is not a terminal, such as in CI or a pipe.

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

## Migrating from create_vm.py

The old script read a tab-separated host list and took everything else as
flags. `dovm import` turns that into a spec:

```sh
dovm import hosts/hostname.txt --name k8s --region nyc3 \
  --size s-4vcpu-8gb --role-size manager=s-2vcpu-4gb --ssh-keys 3871344
dovm validate cluster.yaml
```

| create_vm.py | dovm import |
|---|---|
| `hostname.txt` (`host<TAB>role`) | same file; the role may be omitted (defaults to `worker`), `#` comments allowed |
| `--worker-size`, `--cp-size` | `--size` for every role, `--role-size manager=...` per role |
| `--image`, `--region`, `--ssh-keys` | same flags (defaults `ubuntu-24-04-x64`, `nyc3`) |
| `--tag-names` | `--tags` |
| `--enable-monitoring` | `--monitoring` (on by default) |
| `--enable-private-networking` | always on: droplets join the region's VPC |

`--name` is required, because dovm tags droplets with the cluster name. The
spec is written to `cluster.yaml` (`-o FILE`, or `-o -` for stdout). Droplets
made by the old script are not tagged `dovm:<name>`, so `create` reports them
as conflicts instead of adopting them. Tag them by hand
(`doctl compute droplet tag <id> --tag-name dovm:<name>`) to bring them under
dovm.

## Development

```sh
go test -race ./...
go vet ./...
gofmt -l .
```

CI (`.github/workflows/ci.yml`) runs these on every push to `main` and on pull
requests.

**Releasing.** Push a tag such as `v0.1.0`; `.github/workflows/release.yml`
runs GoReleaser to build binaries for Linux, macOS and Windows (amd64 and
arm64) and attach them to a GitHub release. To try the build locally:
`goreleaser release --snapshot --clean` (output goes to `dist/`).

Layout:

| Path | Purpose |
|---|---|
| `cmd/dovm` | entry point |
| `internal/cli` | command definitions |
| `internal/cloud` | DigitalOcean API client: auth, retries, pagination |
| `internal/spec` | spec parsing, static validation, live checks against the account, host list import |
| `internal/engine` | plan (compare spec with existing droplets), create, wait, rollback, destroy |
| `internal/output` | inventory files |
| `internal/tui` | progress dashboard (Bubble Tea) and spec wizard (huh) |
| `examples` | example specs and an old-format host list |
