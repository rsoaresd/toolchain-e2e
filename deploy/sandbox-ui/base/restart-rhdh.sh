#!/bin/bash

IMAGE_REGISTRY=quay.io
REGISTRY_USER=codeready-toolchain
REPOSITORY_NAME=sandbox-rhdh-plugin
DYNAMIC_PLUGIN_VERSION=$(oc get configmap/rhdh-dynamic-plugins -o jsonpath='{ .data.dynamic-plugins\.yaml }' -n rhdh  | grep -oP 'v\d+(?=!)')
while true
do
    IMAGE_VERSION_QUAY=$(skopeo list-tags docker://${IMAGE_REGISTRY}/${REGISTRY_USER}/${REPOSITORY_NAME} |  jq -r '.Tags[]' | grep -E "^${DYNAMIC_PLUGIN_VERSION}$" || true)
    if [ $DYNAMIC_PLUGIN_VERSION = $IMAGE_VERSION_QUAY ]; then
        oc rollout restart deploy rhdh -n rhdh
        oc rollout status deploy rhdh -n rhdh
        break
    else
            echo "The version plugin in sandbox-sre repo does not match with the one at quay.io, Retrying in 10s..."
            sleep 10
    fi
done