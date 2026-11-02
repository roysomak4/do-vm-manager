## DOVM (Digitalocean Virtual Machine (Droplet) Manager)

Create and manage DigitalOcean droplets easily!!

Simply describe the droplets you want using the easy to use `dovm init` command or edit the `yaml` file, and `dovm create`makes whatever is missing, waits until each one of the droplets is up, and writes their IP addresses to files you can use with SSH or Ansible. Running it again is safe: it only creates what is still missing.`dovm destroy` deletes only droplets that dovm created.

- [Quick start](#quick-start): from nothing to running droplets in five steps
- [Reference](#reference): every spec option and command, for power users
- [Development](#development): building, testing, merging and releasing

## Quick start

**1. Install.** Download the binary for your system from
[GitHub releases](https://github.com/roysomak4/do-vm-manager/releases), or
build it with Go 1.27+:

```sh
go install github.com/roysomak4/do-vm-manager/cmd/dovm@latest
```

**2. Connect your DigitalOcean account.** Create a token with read and write
scope under *API → Tokens* in the DigitalOcean control panel, then:

```sh
export DIGITALOCEAN_TOKEN=dop_v1_...
dovm list        # read-only check; prints "No droplets managed by dovm."
```

If you already use `doctl`, dovm picks up its login and you can skip this.

**3. Describe your droplets.** The `init` wizard asks a few questions and
writes the spec file for you:

```sh
dovm init course.yaml
```

It offers the regions, images, sizes (with prices) and SSH keys in your
account, so you only pick from lists. You will be asked for:

- a **cluster name**, such as `course`. dovm tags every droplet with it so it
  can find them later.
- a **region**, an **image** (for example Ubuntu 24.04) and, if you have
  several, a DigitalOcean **project**.
- your **SSH keys**: press space to tick one, then enter. At least one is
  required. If you have none yet, add one under *Settings → Security* first.
- one or more **roles**. A role is a group of droplets that share a size. For
  hostnames, type a list (`web01, web02`) or a pattern (`student*20` gives
  `student01` to `student20`).

Before saving, it checks everything against your account and shows the
monthly cost.

**4. Create the droplets.**

```sh
dovm create course.yaml --dry-run    # free: shows what would be created
dovm create course.yaml              # asks for confirmation, then creates
```

A live dashboard shows each droplet going from *requesting* to *provisioning*
to *ready*, which usually takes one to two minutes. Billing starts here.

**5. Use them.** The IP addresses are written to `output/`:

```sh
cat output/vms_ip_host.txt           # hostname and IP, one per line
ssh root@<ip>
```

**When you are done:**

```sh
dovm status course.yaml              # what exists and what is missing
dovm destroy course.yaml             # type the cluster name to confirm
```

### Common tasks

| I want to… | Run |
|---|---|
| add more droplets | add hostnames to the spec, then `dovm create course.yaml` |
| finish a run that failed or was interrupted | `dovm create course.yaml` again |
| delete one droplet | `dovm destroy course.yaml --host student07` |
| give participants a password login | `dovm create course.yaml --login-user student` ([details](#password-login-for-participants)) |
| see every droplet dovm manages | `dovm list` |
| check a spec without creating anything | `dovm validate course.yaml` |

## Reference

### Spec file

`dovm init` covers the common options. To use the rest, edit the file it
writes, or start from [`examples/cluster.yaml`](examples/cluster.yaml). Every
option:

```yaml
name: example                         # required; droplets are tagged dovm:example
region: nyc3                          # required; region slug
image: ubuntu-24-04-x64               # required; image slug, or a numeric ID for a snapshot
ssh_keys: [3871344, "3b:16:bf:..."]   # key IDs or fingerprints
tags: [example]                       # extra tags for every droplet
monitoring: true                      # DigitalOcean monitoring agent
ipv6: false
vpc_uuid: 00000000-0000-0000-0000-000000000000   # default VPC of the region if omitted
project: My Project                   # name or ID; default project if omitted
roles:                                # required; at least one
  manager:                            # role name, also added as a tag
    size: s-2vcpu-4gb                 # required; size slug
    hosts: [cp01, cp02, cp03]         # required; droplet names
  worker:
    size: s-4vcpu-8gb
    hosts: [vm01, vm02]
    tags: [pool-a]                    # extra tags for this role only
```

| Field | Required | Default | Notes |
|---|---|---|---|
| `name` | yes | | Letters, digits, `-`, `_`, `:`. Every droplet is tagged `dovm:<name>`. |
| `region` | yes | | For example `nyc3`, `fra1`. |
| `image` | yes | | A slug such as `ubuntu-24-04-x64`, or the numeric ID of a snapshot or custom image. The wizard offers public OS images only; set a snapshot by hand. |
| `ssh_keys` | no | none | Key IDs or fingerprints (`doctl compute ssh-key list`). Without any, DigitalOcean emails a root password instead. Required for `--login-user`. |
| `tags` | no | none | Added to every droplet. |
| `monitoring` | no | `false` | |
| `ipv6` | no | `false` | |
| `vpc_uuid` | no | region default | Droplets always get a private IP in a VPC. |
| `project` | no | account default | See [Projects](#projects). |
| `roles.<role>.size` | yes | | For example `s-1vcpu-1gb`. Run `dovm validate` to check it is offered in your region. |
| `roles.<role>.hosts` | yes | | Unique across all roles. Letters, digits, `.` and `-`. |
| `roles.<role>.tags` | no | none | Added to this role's droplets only. |

Each droplet gets the tags `dovm:<name>`, its role name, the spec's `tags` and
its role's `tags`. Unknown fields are rejected, so typos fail loudly.

**Ownership.** There is no local state file. The `dovm:<name>` tag is how dovm
recognizes its droplets: only droplets carrying it are ever adopted or
deleted, so a droplet you made by hand is never touched, even if it has the
same name as a host.

### Projects

Without `project`, droplets go into your account's default project, as
DigitalOcean does on its own. With it, set to a project's name
(case-sensitive) or ID, `create` puts every droplet in the spec into that
project: the ones it creates, and existing ones that are somewhere else. So
adding `project` to a running cluster and running `create` again moves it.
`create --dry-run` and `status` show which droplets would move. Removing
`project` later does not move droplets back.

The project must already exist; `validate` checks this. If moving droplets
fails, the droplets still work, so `create` reports a warning instead of
failing, and running it again retries the move. A token with custom scopes
needs read and update access to projects; dovm makes no project requests for
specs without `project`.

### Authentication

dovm talks to the DigitalOcean API directly; `doctl` is not required. The API
token is taken from the first of:

1. `--token` flag
2. `DIGITALOCEAN_TOKEN` or `DIGITALOCEAN_ACCESS_TOKEN` environment variable
3. doctl's config file for its current auth context, if you have run `doctl auth init`

### Global flags

| Flag | Default | |
|---|---|---|
| `--token` | see above | DigitalOcean API token |
| `--api-timeout` | `30s` | timeout for each API request attempt |
| `--no-tui` | off | plain log lines instead of the dashboard; automatic when output is not a terminal |

### `dovm init [FILE]`

Writes a spec interactively (`FILE` defaults to `cluster.yaml`). It needs a
terminal and will not overwrite an existing file without `--force`.

### `dovm validate SPEC`

Checks the spec, then confirms the region, sizes, image, SSH keys and project
exist in your account and that each size is offered in the region. It prints
the droplets that would be created and an estimated cost. `--offline` skips
the API checks.

### `dovm create SPEC`

```sh
dovm create cluster.yaml --dry-run   # show the plan only
dovm create cluster.yaml             # asks before creating
dovm create cluster.yaml --yes --rollback-on-failure
```

| Flag | Default | |
|---|---|---|
| `--dry-run` | | show what would be created |
| `-y`, `--yes` | | skip the confirmation (required when not run from a terminal) |
| `--rollback-on-failure` | | if a host fails or you press Ctrl-C, delete the droplets this run created |
| `--wait-timeout` | `10m` | how long to wait for droplets to become active |
| `--output-dir` | `output` | where inventory files are written |
| `--login-user` | | add a password-login user to new droplets |
| `--login-password-file` | | file holding that user's password |

`create` lists your droplets once and decides per host:

| Found | Action |
|---|---|
| no droplet with that name | create it |
| one droplet with that name, tagged `dovm:<name>` | leave it as is (differences in size or region are reported, not changed) |
| a droplet with that name *without* the tag, or several tagged | conflict: nothing is created until you resolve it |

Missing droplets are created in batches of up to 10 per role, then dovm waits
until each is `active` with a public IP. It exits non-zero if any host is not
ready.

**Output files.** When it finishes, `create` writes these for the ready hosts.
Running it again with nothing to create refreshes them.

- `vms.json`: hostname, role, id, public/private IPs, region, size, tags
- `vms_ip_host.txt`: `hostname<TAB>public-ip`
- `vm_ips.txt`: one public IP per line
- `inventory.ini`: Ansible inventory with one group per role, `ansible_host`
  set to the public IP and `ansible_user=root`
- `droplets.json`: raw API objects

**Dashboard.** In a terminal, `create` and `destroy` show one row per droplet
with its state, ID, IP and elapsed time, plus a progress bar and recent
events. Ctrl-C cancels, and the dashboard stays up until cleanup finishes.

**Failures and re-runs.** Existing tagged droplets are left alone, so running
`create` again after a failure, timeout or Ctrl-C only creates what is still
missing. With `--rollback-on-failure`, a failed or interrupted run instead
deletes every droplet *that run* created, never ones that existed before it.

**Retries.** Read requests are retried with exponential backoff on rate
limiting (429) and server errors (5xx), honoring `Retry-After`. A 5xx on a
create request can mean the droplets were made anyway, so dovm checks which
ones now exist and only retries the rest. If that check itself fails, it stops
rather than risk creating duplicates.

#### Password login for participants

For a course or workshop, `--login-user` adds an ordinary user (no sudo) to
every droplet this run creates. That user logs in over SSH with a password,
the same on all droplets, while root stays SSH-key only for admins:

```sh
dovm create course.yaml --login-user student         # prompts for the password twice
DOVM_LOGIN_PASSWORD=... dovm create course.yaml --yes --login-user student
dovm create course.yaml --login-user student --login-password-file pw.txt
```

The spec must list `ssh_keys`. The user is set up by cloud-init on first boot,
so password logins start working about a minute after the droplet shows as
active. The password is never written to the spec or the output files, but it
is stored in plain text in the droplet's user data, which anyone logged in to
the droplet can read from the metadata service. Droplets that already exist
are not changed, so when re-running `create` to finish a cluster, pass the same
user and password.

### `dovm status SPEC` and `dovm list`

```sh
dovm status cluster.yaml          # spec vs. reality, one row per host
dovm list                         # every dovm-managed droplet, all clusters
dovm list --cluster example -o json
```

`status` shows each host as `exists` (with droplet status, IPs and any size or
region drift), `missing` or `conflict`, and lists **orphans**: droplets tagged
for the cluster whose host has been removed from the spec. Both take
`-o json` for scripting.

### `dovm destroy [SPEC]`

```sh
dovm destroy cluster.yaml --dry-run          # show what would be deleted
dovm destroy cluster.yaml                    # delete the spec's hosts
dovm destroy cluster.yaml --host vm02        # just one host (repeatable)
dovm destroy cluster.yaml --include-orphans  # also delete orphans
dovm destroy --name example                  # no spec: every droplet tagged dovm:example
```

You are asked to type the cluster name to confirm; `--yes` skips this, and
without a terminal destroy refuses unless `--yes` is given. Inventory files in
`output/` are not changed.

## Development

### Setup and checks

Requires Go 1.27+. From a checkout:

```sh
go build -o dovm ./cmd/dovm
go test -race ./...
go vet ./...
gofmt -l .          # should print nothing
```

The tests use a fake DigitalOcean API, so they need no token and cost
nothing. Put local specs in `hosts/`; it and `output/` are ignored by git.

| Path | Purpose |
|---|---|
| `cmd/dovm` | entry point |
| `internal/cli` | command definitions |
| `internal/cloud` | DigitalOcean API client: auth, retries, pagination |
| `internal/spec` | spec parsing, static validation, live checks against the account |
| `internal/engine` | plan (compare spec with existing droplets), create, wait, rollback, destroy |
| `internal/cloudinit` | user data for `--login-user` |
| `internal/output` | inventory files |
| `internal/tui` | progress dashboard (Bubble Tea) and spec wizard (huh) |
| `examples` | example specs |

### Branches and merging

1. Branch from `main` for each change.
2. Commit with [Conventional Commits](https://www.conventionalcommits.org/)
   prefixes (`feat:`, `fix:`, `docs:`, `test:`, `chore:`). Release notes are
   built from commit messages, and `docs:`, `test:` and `chore:` commits are
   left out of them.
3. Push and open a pull request against `main`. CI
   (`.github/workflows/ci.yml`) checks `gofmt`, runs `go vet` and
   `go test -race`, and builds the binary. It runs on every pull request and
   every push to `main`.
4. Before merging anything that changes how droplets are created or deleted,
   test it on a real account (below).
5. Merge once CI is green.

### Testing on a real account

This creates three of the smallest droplets (about $0.03/hour in total) from
[`examples/smoke-test.yaml`](examples/smoke-test.yaml) and deletes them
again. Everything before `create` is free.

```sh
go build -o dovm ./cmd/dovm
mkdir -p hosts && cp examples/smoke-test.yaml hosts/test.yaml
# edit hosts/test.yaml: ssh_keys: [<your key ID>]

./dovm validate hosts/test.yaml
./dovm create hosts/test.yaml --dry-run
./dovm create hosts/test.yaml                     # 3 droplets; costs money from here
./dovm create hosts/test.yaml                     # "0 to create, 3 already exist"
./dovm destroy hosts/test.yaml --host test-vm02   # type "dovm-test"
./dovm create hosts/test.yaml                     # recreates only test-vm02
```

Things worth checking:

- **Orphans:** remove `test-vm02` from the spec, and `status` lists it as an
  orphan; `destroy --include-orphans` deletes it.
- **Projects:** add `project: <one of yours>`; `create --dry-run` shows
  "move to project ...", and `create` moves the droplets.
- **Interrupting:** after `destroy`, run
  `create --yes --rollback-on-failure` and press Ctrl-C while rows show
  *provisioning*. Everything this run made is deleted. Without
  `--rollback-on-failure`, droplets already made are kept, and the next
  `create` finishes the job.
- **Plain output:** `status -o json` and `create --yes --no-tui`.

Clean up with `./dovm destroy hosts/test.yaml`, then confirm `./dovm list`
prints "No droplets managed by dovm." If something goes wrong mid-test,
`./dovm destroy --name dovm-test` deletes every droplet tagged
`dovm:dovm-test` without needing the spec.

### Releasing

Releases are cut from `main` by pushing a [semver](https://semver.org/) tag:

```sh
git checkout main && git pull
git tag v0.1.0
git push origin v0.1.0
```

`.github/workflows/release.yml` then runs the tests and
[GoReleaser](https://goreleaser.com/) (`.goreleaser.yaml`), which builds
binaries for Linux, macOS and Windows (amd64 and arm64), stamps the version
into `dovm --version`, and attaches them with checksums and release notes to
a GitHub release. To try the build locally without publishing:

```sh
goreleaser release --snapshot --clean    # output in dist/
```

## Acknowledgement

This code base is a rewrite of the older python code in Golang for high performance and stability. Claude Code was used for developing automated unit tests, CI, github actions, and documentation. App behavior and end to end testing was performed and confirmed manually against digitalocean platform. For feature requests or bug reports, please open an Issue in this repo.

## License and disclaimer

Licensed under the [MIT License](LICENSE). The software is provided "as is",
without warranty of any kind, and the author accepts no liability for any
damage or harm arising from its use; see the license for the full terms.

`dovm` creates and destroys billable resources in your DigitalOcean account
using the API token you supply. You are solely responsible for any charges
incurred, any data lost through deleted droplets, and the security of the
droplets it creates (including SSH keys and passwords configured on them).
Review your spec files and the commands you run before using this tool
against an account that holds resources you care about.
