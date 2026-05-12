from __future__ import print_function
import time
import kubernetes.client
import kubernetes.config
from kubernetes.client.rest import ApiException
from pprint import pprint
import sys
import yaml

kubernetes.config.load_kube_config()

api_instance = kubernetes.client.CoreV1Api()

name = 'dnsmasq'
namespace = 'default'
pretty = 'true'
type = sys.argv[1]
deployment_file = "/holodeck-runtime/dnsmasq/dnsmasq_deployment.yaml"

try:
    if type == "create":
        dns_entry = sys.argv[2]
        api_response = api_instance.read_namespaced_config_map(name, namespace, pretty=pretty)
        if dns_entry in api_response.data['hosts']:
            sys.exit("The DNS entry already exists in the DNS server")
        else:
            api_response.data['hosts'] += '\n' + dns_entry
    elif type == "update":
        search_dns_entry = sys.argv[2]
        replace_dns_entry = sys.argv[3]
        api_response = api_instance.read_namespaced_config_map(name, namespace, pretty=pretty)
        if search_dns_entry in api_response.data['hosts']:
            api_response.data['hosts'] = api_response.data['hosts'].replace(search_dns_entry, replace_dns_entry)
        else:
            sys.exit("The DNS entry does not exist in the DNS server")
except ApiException as e:
    print("Exception when calling CoreV1Api->read_namespaced_config_map: %s\n" % e)


body = api_response
try:
    api_patch_response = api_instance.patch_namespaced_config_map(name, namespace, body, pretty=pretty)
except ApiException as e:
    print("Exception when calling CoreV1Api->patch_namespaced_config_map: %s\n" % e)

api_apps_instance = kubernetes.client.AppsV1Api()

# Parse all YAML documents from the deployment file
deployments = []
try:
    with open(deployment_file) as f:
        # Use safe_load_all to handle multiple YAML documents
        yaml_docs = yaml.safe_load_all(f)
        for doc in yaml_docs:
            if doc and doc.get('kind') == 'Deployment':
                deployments.append(doc)
except Exception as e:
    print(f"Exception when reading deployment file: %s\n" % e)
    sys.exit(1)

# Delete all deployments found in the file
for dep in deployments:
    dep_name = dep['metadata']['name']
    try:
        namespace_delete_response = api_apps_instance.delete_namespaced_deployment(dep_name, namespace)
        print(f"Deleted deployment: {dep_name}")
    except ApiException as e:
        # Ignore 404 errors (deployment doesn't exist)
        if e.status != 404:
            print(f"Exception when calling AppsV1Api->delete_namespaced_deployment for {dep_name}: %s\n" % e)

# Wait a moment for deletions to complete
time.sleep(2)

# Recreate all deployments from the file
for dep in deployments:
    dep_name = dep['metadata']['name']
    try:
        deployment_response = api_apps_instance.create_namespaced_deployment(body=dep, namespace=namespace)
        print(f"Created deployment: {dep_name}")
    except ApiException as e:
        print(f"Exception when calling AppsV1Api->create_namespaced_deployment for {dep_name}: %s\n" % e)

if deployments:
    print(f"DNS Record successfully updated")
