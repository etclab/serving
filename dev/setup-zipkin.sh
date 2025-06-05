#!/bin/bash

kubectl create namespace zipkin-monitoring

kubectl create deployment zipkin --image openzipkin/zipkin -n zipkin-monitoring

kubectl expose deployment zipkin --type ClusterIP --port 9411 -n zipkin-monitoring