#!/bin/bash

user_help () {
    echo "Generates the kubesaw-admins.yaml file with the provided parameters"
    echo "options:"
    echo "-hn,  --host-namespace     Host Operator namespace"
    echo "-mn,  --member-namespace   Member Operator namespace"
    echo "-h,   --help               To show this help text"
    echo ""
    exit 0
}

read_arguments() {
    if [[ $# -lt 2 ]]
    then
        echo "There are missing parameters"
        user_help
    fi

    while test $# -gt 0; do
           case "$1" in
                -h|--help)
                    user_help
                    ;;
                -hn|--host-namespace)
                    shift
                    HOST_NS=$1
                    shift
                    ;;
                -mn|--member-namespace)
                    shift
                    MEMBER_NS=$1
                    shift
                    ;;
                *)
                   echo "$1 is not a recognized flag!" >> /dev/stderr
                   user_help
                   exit -1
                   ;;
          esac
    done
}

set -e

read_arguments $@

TMP_DIR="scripts/ksctl/bin"

# Delete TMP_DIR if it exists
rm -rf "$TMP_DIR"

# Recreate TMP_DIR
mkdir -p "$TMP_DIR"

KUBESAW_ADMINS_PATH="$TMP_DIR/kubesaw-admins.yaml"

# kustomize kubesaw-admins.yaml
API=$(oc whoami --show-server) HOST_NS=${HOST_NS} MEMBER_NS=${MEMBER_NS} envsubst < scripts/ksctl/kubesaw-admins.yaml > "$KUBESAW_ADMINS_PATH"

ADMINS_MANIFESTS_PATH="$TMP_DIR/admin-manifests"

# run the ksctl command with the provided arguments
ksctl generate admin-manifests --kubesaw-admins "$KUBESAW_ADMINS_PATH" --out-dir "$ADMINS_MANIFESTS_PATH"
echo "Admin manifests generated successfully in: $ADMINS_MANIFESTS_PATHs"

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
    
    if oc get namespace "$NAMESPACE" &>/dev/null; then
        echo "Namespace $NAMESPACE already exists, skipping..."
    else
        oc create ns "$NAMESPACE"
    fi
done
echo "All namespaces created successfully!"

SUBDIRS=(
    "host"
    "member"
)
for SUBDIR in "${SUBDIRS[@]}"; do
    echo "Applying kustomization in: $ADMINS_MANIFESTS_PATH/$SUBDIR"
    oc apply -k "$ADMINS_MANIFESTS_PATH/$SUBDIR"
done
echo "All kustomizations applied successfully!"


KSCTL_CONFIG_PATH=$HOME/out/config

# generate ksctl.yaml file
ksctl generate cli-configs -k "$KUBECONFIG" -c "$KUBESAW_ADMINS_PATH" -o $KSCTL_CONFIG_PATH


# copy first-admin/ksctl.yaml to $HOME/.ksctl.yaml (default expected by ksctl)
cp $KSCTL_CONFIG_PATH/first-admin/ksctl.yaml $HOME/.ksctl.yaml
