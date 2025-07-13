#!/bin/bash

ns=default

# these overridden values (name/id) are used internally for the etcd sync and 
# lease management, as Knative doesn't provide a way to choose a specific replica
# serviceA:alt_nameA,serviceB:alt_nameA -- we're basically forcing two services 
# to be part of the same lease group as well use the same public params
# ALT_SERVICE_NAMES="appender-ego-member:appender-ego-svc,appender-ego-leader:appender-ego-svc"
# ALT_FUNCTION_IDS="appender-ego-member:appender-ego-fun,appender-ego-leader:appender-ego-fun"

# FUNCTION_CHAIN="validate-fun/vote-fun/count-vote-fun/display-fun"
ALT_SERVICE_NAMES="validate-fun-member:validate-fun-svc,validate-fun-leader:validate-fun-svc\
,vote-fun-member:vote-fun-svc,vote-fun-leader:vote-fun-svc\
,count-vote-fun-member:count-vote-fun-svc,count-vote-fun-leader:count-vote-fun-svc\
,display-fun-member:display-fun-svc,display-fun-leader:display-fun-svc"

ALT_FUNCTION_IDS="validate-fun-member:validate-fun-id,validate-fun-leader:validate-fun-id\
,vote-fun-member:vote-fun-id,vote-fun-leader:vote-fun-id\
,count-vote-fun-member:count-vote-fun-id,count-vote-fun-leader:count-vote-fun-id\
,display-fun-member:display-fun-id,display-fun-leader:display-fun-id"

# service-config secret is used to provide fixed service name or function id
kubectl delete secret service-config -n $ns --ignore-not-found=true

kubectl create secret generic service-config -n $ns \
  --from-literal=service_names="${ALT_SERVICE_NAMES}" \
  --from-literal=function_ids="${ALT_FUNCTION_IDS}"