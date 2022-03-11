# Digitalocean VM management script using doctl tool

This script automates creation of droplets on digitalocean cloud platform.

Requirements

1. Linux or Macos
2. `doctl` CLI tool provided by digitalocean should be installed
3. `doctl` should be authenticated to access the digitalocean resources for a given account. Account can be authenticated using

```
doctl auth init -t <Access token from digitalocean>
```

4. `sh` python package is required. It can be installed using `pip install -U sh`.
5. A tab delimited file with list of hostnames and roles. An example of the input file

```
# hostname.txt
vm01    worker
vm02    worker
```

The acceptable value for roles are `worker` and `manager`. These keywords are mainly used for creating VMS for high availability kubernetes clusters. For general usage, `worker` should be used for the role.

## Usage

Full usage instructions can be found by simply running the script without any arguments

```
$ python3 create_vm.py
usage: create_vm.py [-h] [--image IMAGE] [--cp-size CP_SIZE] [--worker-size WORKER_SIZE] [--region REGION]
                    [--tag-names TAG_NAMES] [--ssh-keys SSH_KEYS]
                    [--enable-private-networking [ENABLE_PRIVATE_NETWORKING]]
                    [--enable-monitoring [ENABLE_MONITORING]]
                    hostnames
create_vm.py: error: the following arguments are required: hostnames
```

Example command

```
python3 create_vm.py hosts/hostname.txt --image ubuntu-20-04-x64 --worker-size s-4vcpu-8gb-intel --region nyc3 --tag-names example --ssh-keys 3871344,9918291
```

When the program runs successfully, three output files are generates under `output` folder

1. `vms.json`
2. `vms_ip_host.txt`
3. `vm_ips.txt`

These outputs are relevant for different downstream usages.
