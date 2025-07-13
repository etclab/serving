#!/bin/bash

ns=default

# these overridden values (name/id) are used internally for the etcd sync and 
# lease management, as Knative doesn't provide a way to choose a specific replica
# serviceA:alt_nameA,serviceB:alt_nameA -- we're basically forcing two services 
# to be part of the same lease group as well use the same public params
ALT_SERVICE_NAMES="appender-ego-member:appender-ego-svc,appender-ego-leader:appender-ego-svc"
ALT_FUNCTION_IDS="appender-ego-member:appender-ego-fun,appender-ego-leader:appender-ego-fun"

# service-config secret is used to provide fixed service name or function id
kubectl delete secret service-config -n $ns --ignore-not-found=true

kubectl create secret generic service-config -n $ns \
  --from-literal=service_names="${ALT_SERVICE_NAMES}" \
  --from-literal=function_ids="${ALT_FUNCTION_IDS}"