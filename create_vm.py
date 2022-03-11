import argparse
import json
import os
import sh
from time import sleep


def create_vms():
    # get arguments
    args = parse_arguments()

    # get hostnames from the file provided
    hosts = get_hostnames(args.hostnames)
    server_count = len(hosts)
    print(f"Creating {server_count} virtual machines.")

    vms_created = False
    
    # loop through the host names and execute digitalocean API to create VMs
    for host in hosts:
        hostname = host[0]
        role = host[1]
        if not host_exists(hostname):
            cmd = f'doctl compute droplet create {hostname} --image {args.image} --region {args.region} --tag-names "{role}"'
            if role == 'manager':
                cmd += f' --size {args.cp_size}'
            else:
                cmd += f' --size {args.worker_size}'
            if args.enable_private_networking:
                cmd += ' --enable-private-networking'
            if args.enable_monitoring:
                cmd += ' --enable-monitoring'
            if args.ssh_keys.strip() != '':
                cmd += f' --ssh-keys {args.ssh_keys}'
            print(f'Initializing creation of VM {host}...')
            sh.bash("-c", cmd)
            print(f'VM creation initialized for {host}.')
            vms_created = True
        else:
            print(f'{host} already exists in digitalocean. Aborted VM creation for this hostname.')
    
    save_vm_info(server_count, hosts)

    
def save_vm_info(server_count, new_hosts):
    # wait for 5 seconds before pinging digitalocean for newly created servers
    print('Waiting for 5 seconds before querying info from newly created VMs on digitalocean...')
    sleep(5)

    vms_available = False
    max_tries = 5
    tries = 0

    # get info for the created servers and write to file
    print('Querying digitalocean for newly created VMs...')
    while not vms_available:
        tries += 1
        if tries > max_tries:
            print('Something went wrong. Cannot retrieve server information.')
            print('Please check on digitalocean dashboard.')
            exit()
        else:
            servers = get_vm_info(new_hosts)
            if len(servers) >= server_count:
                print('Info for newly created VMs retrieved successfully.')
                # write the server info to output json file
                print('Saving VM info to output file...')
                save_server_info(servers)
                print('VM info written to vms.json')
                vms_available = True
            else:
                print('Some servers are still initializing. Trying again in 5 seconds...')
                sleep(5)


def get_vm_info(new_hosts):
    vm_info = sh.doctl.compute.droplet.ls("-o", "json")
    servers = []
    if vm_info.stdout.decode('utf-8') != 'null':
        vms = json.loads(vm_info.stdout.decode('utf-8'))
        hostnames = get_hostlist(new_hosts)
        for vm in vms:
            if vm['name'] in hostnames:
                server = {}
                server['hostname'] = vm['name']
                networks = vm['networks']['v4']
                for network in networks:
                    server[network['type']] = network['ip_address']
                server['role'] = vm['tags'][0]
                servers.append(server)
    return servers


def get_hostlist(new_hosts):
    retinfo = []
    for host in new_hosts:
        retinfo.append(host[0])
    return retinfo


def get_vm_hostname():
    vm_info = sh.doctl.compute.droplet.ls("-o", "json")
    hostnames = []
    if vm_info.stdout.decode('utf-8') != 'null':
        vms = json.loads(vm_info.stdout.decode('utf-8'))
        for vm in vms:
            hostnames.append(vm['name'])
    return hostnames


def host_exists(hostname):
    retinfo = False
    servers = get_vm_hostname()
    if len(servers) > 0:
        if hostname in servers:
            retinfo = True
    return retinfo


def save_server_info(servers):
    # check if the "output" directory exists
    if not os.path.isdir("output"):
        os.mkdir("output")
    # create JSON output
    with open('output/vms.json', 'w') as vmf:
        json.dump(servers, vmf, indent=2)
    # create tab delimited file with hostname and public ip address list
    with open('output/vms_ip_host.txt', 'w') as vmf:
        for server in servers:
            vmf.write(f'{server["hostname"]}\t{server["public"]}\n')
    # create file with list of public IPs
    with open('output/vm_ips.txt', 'w') as vmf:
        for server in servers:
            vmf.write(f'{server["public"]}\n')
            

def parse_arguments():
    parser = argparse.ArgumentParser(formatter_class=argparse.ArgumentDefaultsHelpFormatter)
    parser.add_argument('hostnames',
                        type=str,
                        help='File containing list of hostnames. Required field.')
    parser.add_argument('--image',
                        type=str,
                        help='Image for the VMs. E.g ubuntu-18-04-x64',
                        default='ubuntu-18-04-x64')
    parser.add_argument('--cp-size',
                        type=str,
                        help='VM size. E.g. s-2vcpu-2gb',
                        default='s-2vcpu-2gb')
    parser.add_argument('--worker-size',
                        type=str,
                        help='VM size. E.g. s-2vcpu-2gb',
                        default='s-2vcpu-2gb')
    parser.add_argument('--region',
                        type=str,
                        help='Datacenter region where the VMs will be created.',
                        default='nyc1')
    parser.add_argument('--tag-names',
                        type=str,
                        help='Tag names for the VMs',
                        default='')
    parser.add_argument('--ssh-keys',
                        type=str,
                        help='SSH key ID or fingerprint in digitalocean. This info can be obtained from DO dashboard or by running "doctl compute ssh-keys ls" command.',
                        default='')
    parser.add_argument('--enable-private-networking',
                        type=str2bool,
                        help='Enable private networking',
                        default=False,
                        const=True,
                        nargs='?')
    parser.add_argument('--enable-monitoring',
                        type=str2bool,
                        help='Enable droplet monitoring',
                        default=False,
                        const=True,
                        nargs='?')

    return parser.parse_args()


def str2bool(arg):
    '''
    This module handles boolean argument input
    Source: https://stackoverflow.com/questions/15008758/parsing-boolean-values-with-argparse
    '''
    if isinstance(arg, bool):
        return arg
    elif arg.lower() in ['yes', 'y', '1', 'true', 't']:
        return True
    elif arg.lower() in ['no', 'n', '0', 'false', 'f']:
        return False
    else:
        raise argparse.ArgumentTypeError('Boolean value expected.')


def get_hostnames(filename):
    hosts = []
    with open(filename, 'r') as f:
        for line in f:
            lineitem = line.strip('\n').strip()
            if lineitem != '':
                hosts.append(lineitem.split('\t'))
    return hosts


if __name__ == '__main__':
    create_vms()
