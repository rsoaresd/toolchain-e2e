#!/bin/bash

user_help () {
    echo "Usage: $0 <kubesaw-admins-path> <admin-manifests-out-dir-path> <kubeconfig-path>"
    echo "  <kubesaw-admins-path>: Path to the kubesaw-admins.yaml file"
    echo "  <admin-manifests-out-dir-path>: Path to the output directory containing kustomization files"
    echo "  <kubeconfig-path>: Path to the kubeconfig file"
    exit 0
}

read_arguments() {
 if [[ $# -lt 3 ]]
    then
        echo "There are missing parameters"
        user_help
    fi
}

set -e

read_arguments $@


KUBESAW_ADMINS_PATH=scripts/ksctl/kubesaw-admins.yaml

# kustomize kubesaw-admins.yaml
kustomize build scripts/ksctl/generate-ksctl-file | API=${API} HOST_NS=${HOST_NS} MEMBER_NS=${MEMBER_NS} envsubst | oc apply -f -

# run the ksctl command with the provided arguments
ksctl generate admin-manifests --kubesaw-admins "$KUBESAW_ADMINS_PATH" --out-dir "$OUT_DIR_PATH"
echo "Admin manifests generated successfully in: $OUT_DIR_PATH"

# create resources from the <admin-manifests-out-dir-path>
NAMESPACES=(
    "host-sre-namespace"
    "first-component"
    "second-component"
    "some-component"
    "member-sre-namespace"
    "crw"
)
for NAMESPACE in "${NAMESPACES[@]}"; do
    echo "Creating namespace: $NAMESPACE"
    oc create ns "$NAMESPACE"
done
echo "All namespaces created successfully!"

SUBDIRS=(
    "host"
    "member"
    "member-3"
)
for SUBDIR in "${SUBDIRS[@]}"; do
    echo "Applying kustomization in: $OUT_DIR_PATH/$SUBDIR"
    oc apply -k "$OUT_DIR_PATH/$SUBDIR"
done
echo "All kustomizations applied successfully!"


# generate ksctl.yaml file
ksctl generate cli-configs -k "$KUBECONFIG_PATH" -c "$KUBESAW_ADMINS_PATH"
