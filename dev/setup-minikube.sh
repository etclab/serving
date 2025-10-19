#!/bin/bash

minikube delete -p knative

minikube addons enable metrics-server

kn quickstart minikube --kubernetes-version=v1.32.0


minikube profile knative