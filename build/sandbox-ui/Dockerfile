FROM registry.access.redhat.com/ubi9/ubi:latest as build-tools

LABEL maintainer="Developer Sandbox <devsandbox@redhat.com>"
LABEL author="Developer Sandbox <devsandbox@redhat.com>"

ENV GOPATH=/tmp/go \
    GOCACHE=/tmp/.cache \
    PATH=$PATH:$GOPATH/bin \
    GIT_COMMITTER_NAME="Developer Sandbox" \
    GIT_COMMITTER_EMAIL=devsandbox@redhat.com \
    GOLANG_VERSION=go1.22.12 \
    GOLANG_SHA256=4fa4f869b0f7fc6bb1eb2660e74657fbf04cdd290b5aef905585c86051b34d43

# Install additional tools
# gettext for envsubst
RUN yum install -y \
        findutils \
        git \
        make \
        gettext \
        procps-ng \
        tar \
        wget \
        which \
        jq \
        gcc \
        gcc-c++ \
        python3 \
        python3-devel \
        sqlite-devel && \
    yum clean all

# Install kustomize
RUN curl -s https://raw.githubusercontent.com/kubernetes-sigs/kustomize/master/hack/install_kustomize.sh | bash \
    && mv kustomize /usr/local/bin

# Install Node.js 22 and yarn
RUN curl -fsSL https://rpm.nodesource.com/setup_22.x | bash - && \
    yum install -y nodejs && \
    curl -sL https://dl.yarnpkg.com/rpm/yarn.repo -o /etc/yum.repos.d/yarn.repo && \
    yum install -y yarn && \
    yum clean all

# Install oc CLI
RUN curl -LO https://mirror.openshift.com/pub/openshift-v4/clients/ocp/latest/openshift-client-linux.tar.gz && \
    tar -xzvf openshift-client-linux.tar.gz -C /usr/local/bin oc && \
    chmod +x /usr/local/bin/oc && \
    rm -f openshift-client-linux.tar.gz

# Download, verify, and install Go
ENV PATH=$PATH:/usr/local/go/bin
WORKDIR /tmp
RUN curl -Lo ${GOLANG_VERSION}.linux-amd64.tar.gz https://dl.google.com/go/${GOLANG_VERSION}.linux-amd64.tar.gz \
    && echo "${GOLANG_SHA256} ${GOLANG_VERSION}.linux-amd64.tar.gz" > ${GOLANG_VERSION}.linux-amd64.sha256 \
    && sha256sum -c ${GOLANG_VERSION}.linux-amd64.sha256 \
    && tar -C /usr/local -xzf ${GOLANG_VERSION}.linux-amd64.tar.gz \
    && rm -f ${GOLANG_VERSION}.linux-amd64.tar.gz \
    && go version

WORKDIR /app

# Increase shared memory for browsers (recommended for Playwright)
RUN mkdir -p /dev/shm && chmod 1777 /dev/shm

COPY . .

# Install Firefox required dependencies
RUN yum install -y \
        libxcb \
        libX11 \
        libXext \
        libXrandr \
        libXcomposite \
        libXcursor \
        libXdamage \
        libXfixes \
        libXi \
        gtk3 \
        pango \
        atk \
        cairo \
        gdk-pixbuf2 \
        libXrender \
        alsa-lib \
        freetype \
        fontconfig && \
    yum clean all

CMD ["make", "test-ui-e2e"]
