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

try:  
    api_response = api_instance.read_namespaced_config_map(name, namespace, pretty=pretty)
#    dns_entries = [item.strip() for item in api_response.data['hosts'].splitlines() if item.strip()]
    print(api_response.data['hosts'])
except ApiException as e:
    print("Exception when calling CoreV1Api->read_namespaced_config_map: %s\n" % e)
