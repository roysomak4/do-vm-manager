import argparse
import json
import sh
from time import sleep


def create_vms():
    # get arguments
    args = parse_arguments()

    # get hostnames from the file provided
    hosts = get_hostnames(args.hostnames)

    vms_created = False

    # loop through the host names and execute digitalocean API to create VMs
    for host in hosts:
        if not host_exists(host):
            cmd = f'doctl compute droplet create {host} --image {args.image} --size {args.size} --region {args.region}'
            if args.tag_name.strip() != '':
                cmd += f' --tag-name {args.tag_name}'
            if args.enable_private_networking:
                cmd += ' --enable-private-networking'
            if args.enable_monitoring:
                cmd += ' --enable-monitoring'
            sh.bash("-c", cmd)
            print(f'VM creation initialized for {host}.')
            vms_created = True
        else:
            print(f'{host} already exists in digitalocean. Aborted VM creation for this hostname.')

    if vms_created:
        # wait for 3 seconds before pinging digitalocean for newly created servers
        print('Waiting for 3 seconds before querying info from newly created VMs on digitalocean...')
        sleep(3)

        # get info for the created servers and write to file
        print('Querying digitalocean for newly created VMs...')
        servers = get_vm_info()
        if len(servers) > 0:
            print('Info for newly created VMs retrieved successfully.')
            print(servers)
            # write the server info to output json file
            print('Saving VM info to output file...')
            save_server_info(servers)
            print('VM info written to vms.json')
        else:
            print('No server info retrieved.')
    else:
        print('No new VMs were created.')


def get_vm_info():
    vm_info = sh.doctl.compute.droplet.ls("-o", "json")
    servers = []
    if vm_info.stdout.decode('utf-8') != 'null':
        vms = json.loads(vm_info.stdout.decode('utf-8'))
        for vm in vms:
            server = {}
            server['hostname'] = vm['name']
            networks = vm['networks']['v4']
            for network in networks:
                server[network['type']] = network['ip_address']
            servers.append(server)
    return servers


def host_exists(hostname):
    retinfo = False
    servers = get_vm_info()
    if len(servers) > 0:
        for server in servers:
            if hostname in server:
                retinfo = True
                break
    else:
        retinfo = False
    return retinfo


def save_server_info(servers):
    with open('vms.json', 'w') as vmf:
        json.dump(servers, vmf, indent=2)


def parse_arguments():
    parser = argparse.ArgumentParser(formatter_class=argparse.ArgumentDefaultsHelpFormatter)
    parser.add_argument('hostnames',
                        type=str,
                        help='File containing list of hostnames. Required field.')
    parser.add_argument('--image',
                        type=str,
                        help='Image for the VMs. E.g ubuntu-18-04-x64',
                        default='ubuntu-18-04-x64')
    parser.add_argument('--size',
                        type=str,
                        help='VM size. E.g. s-2vcpu-2gb',
                        default='s-2vcpu-2gb')
    parser.add_argument('--region',
                        type=str,
                        help='Datacenter region where the VMs will be created.',
                        default='nyc3')
    parser.add_argument('--tag-name',
                        type=str,
                        help='Tag names for the VMs',
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
            if line.strip('\n').strip() != '':
                hosts.append(line.strip('\n').strip())
    return hosts


if __name__ == '__main__':
    create_vms()
