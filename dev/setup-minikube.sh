#!/bin/bash

minikube delete -p knative

kn quickstart minikube

minikube profile knative