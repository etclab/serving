#!/bin/bash

function delete_function() {
    # $1 is function yaml file    
    # $2 is namespace
    kubectl delete -f $1 -n $2
    kubectl wait --for=delete pods --all -n $2 --timeout=300s
}

function deploy_function() {
    # $1 is function yaml file    
    # $2 is namespace
    kubectl apply -f $1 -n $2
    kubectl wait --for=condition=ready services.serving.knative.dev --all -n $2 --timeout=300s
    kubectl wait --for=condition=ready pods --all -n $2 --timeout=300s
}