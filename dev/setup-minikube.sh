#!/bin/bash

minikube delete -p knative

minikube addons enable metrics-server

minikube config set driver docker
minikube config set memory 32768
minikube config set cpus 16

kn quickstart minikube --kubernetes-version=v1.33.0


minikube profile knative