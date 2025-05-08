NAMESPACE := sandbox-ui14
SANDBOX_PLUGIN_IMAGE_NAME := sandbox-rhdh-plugin

.PHONY: deploy-sandbox-ui
deploy-sandbox-ui: REGISTRATION_SERVICE_API=https://$(shell oc get route registration-service -n ${HOST_NS} -o custom-columns=":spec.host" | tr -d '\n')/api/v1
deploy-sandbox-ui: HOST_OPERATOR_API=https://$(shell oc get route api -n ${HOST_NS} -o custom-columns=":spec.host" | tr -d '\n')
deploy-sandbox-ui:
	@echo "sandbox ui will be deployed in '${NAMESPACE}' namespace"
	$(MAKE) create-namespace NAMESPACE=${NAMESPACE}
	$(MAKE) push-sandbox-plugin
	kustomize build deploy/sandbox-ui/e2e-tests | REGISTRATION_SERVICE_API=${REGISTRATION_SERVICE_API} HOST_OPERATOR_API=${HOST_OPERATOR_API} NAMESPACE=${NAMESPACE} SANDBOX_PLUGIN_IMAGE=${OS_IMAGE_REGISTRY}/${NAMESPACE}/${SANDBOX_PLUGIN_IMAGE_NAME}:latest envsubst | oc apply -f -
	@oc -n ${NAMESPACE} rollout status deploy/rhdh


create-namespace:
	@if ! oc get project ${NAMESPACE} >/dev/null 2>&1; then \
		echo "Creating namespace ${NAMESPACE}"; \
		oc new-project ${NAMESPACE} >/dev/null 2>&1 || true; \
	else \
		echo "Namespace ${NAMESPACE} already exists"; \
	fi
	@oc project ${NAMESPACE} >/dev/null 2>&1


OS := $(shell uname -s)
ARCH := $(shell uname -m)
PLATFORM ?= $(OS)/$(ARCH)
RHDH_PLUGINS_DIR := "$(TMPDIR)rhdh-plugins"

.PHONY: clone-rhdh-plugins
clone-rhdh-plugins:
	rm -rf ${RHDH_PLUGINS_DIR}; \
	git clone https://github.com/redhat-developer/rhdh-plugins $(RHDH_PLUGINS_DIR) && \
	echo "cloned to $(RHDH_PLUGINS_DIR)"

.PHONY: push-sandbox-plugin
push-sandbox-plugin: check-registry
push-sandbox-plugin: IMAGE_TO_PUSH=${OS_IMAGE_REGISTRY}/${NAMESPACE}/${SANDBOX_PLUGIN_IMAGE_NAME}:latest
push-sandbox-plugin:
	$(MAKE) clone-rhdh-plugins
	cd $(RHDH_PLUGINS_DIR)/workspaces/sandbox && \
	echo "podman push ${IMAGE_TO_PUSH} --creds=${OC_WHOAMI}:${OC_WHOAMI_TOKEN} --tls-verify=false" && \
	npx @janus-idp/cli@3.3.1 package package-dynamic-plugins \
		--tag ${IMAGE_TO_PUSH} \
		--platform ${PLATFORM} && \
	podman push ${IMAGE_TO_PUSH} --creds=${OC_WHOAMI}:${OC_WHOAMI_TOKEN} --tls-verify=false