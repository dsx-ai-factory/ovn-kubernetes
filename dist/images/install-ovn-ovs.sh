#!/bin/sh

set -eu

export DEBIAN_FRONTEND=noninteractive

require_noble() {
    . /etc/os-release
    if [ "${VERSION_CODENAME}" != noble ]; then
        echo "${1} packages require Ubuntu 24.04 (noble)" >&2
        exit 1
    fi
}

add_artifactory_repo() {
    artifactory_cert="$(cat /run/secrets/artifactory_cert)"
    curl --fail --show-error --location --retry 3 \
        "https://${artifactory_cert}@artifactory.nvidia.com/artifactory/api/gpg/key/public" \
        -o /usr/share/keyrings/nvidia-artifactory.asc
    printf 'deb [signed-by=/usr/share/keyrings/nvidia-artifactory.asc] https://%s@artifactory.nvidia.com/artifactory/sw-ngn-sdn-debian-local %s main\n' \
        "${artifactory_cert}" "${DIST_CODE}" > /etc/apt/sources.list.d/ngn-sdn-ovn.list
}

add_uca_repo() {
    require_noble UCA
    apt-get install -y --no-install-recommends ubuntu-cloud-keyring
    printf 'deb [signed-by=/usr/share/keyrings/ubuntu-cloud-keyring.gpg] https://ubuntu-cloud.archive.canonical.com/ubuntu noble-updates/%s main\n' \
        "${CLOUD_ARCHIVE}" > /etc/apt/sources.list.d/cloud-archive.list
}

add_doca_repo() {
    require_noble DOCA

    case "${TARGETARCH}" in
        amd64)
            doca_arch=x86_64
            ;;
        arm64)
            doca_arch=arm64-sbsa
            ;;
        *)
            echo "Unsupported DOCA architecture: ${TARGETARCH}" >&2
            exit 1
            ;;
    esac

    doca_repo="https://linux.mellanox.com/public/repo/doca/${DOCA_RELEASE}/ubuntu24.04/${doca_arch}"
    curl --fail --show-error --location --retry 3 \
        "${doca_repo}/doca_keyring.gpg" -o /usr/share/keyrings/doca-keyring.gpg
    printf 'deb [signed-by=/usr/share/keyrings/doca-keyring.gpg] %s /\n' \
        "${doca_repo}" > /etc/apt/sources.list.d/doca.list
}

install_ovs() {
    case "${OVS_SOURCE}" in
        artifactory)
            apt-get install -y --no-install-recommends \
                "openvswitch-switch=${OVS_VER}" "openvswitch-common=${OVS_VER}"
            ;;
        uca)
            apt-get install -y --no-install-recommends \
                -t "noble-updates/${CLOUD_ARCHIVE}" openvswitch-switch openvswitch-common
            ;;
        doca)
            OVS_VER="${OVS_VER:-3.5.0052-1}"
            apt-get install -y --no-install-recommends \
                "doca-openvswitch-switch=${OVS_VER}" "doca-openvswitch-common=${OVS_VER}"
            ;;
        *)
            echo "Unknown OVS_SOURCE: ${OVS_SOURCE}" >&2
            exit 1
            ;;
    esac
}

install_ovn() {
    case "${OVN_SOURCE}" in
        artifactory)
            apt-get install -y --no-install-recommends \
                "ovn-common=${OVN_VER}" "ovn-host=${OVN_VER}" "ovn-central=${OVN_VER}"
            ;;
        uca)
            apt-get install -y --no-install-recommends \
                "ovn-common=${UCA_OVN_VER}" "ovn-host=${UCA_OVN_VER}" \
                "ovn-central=${UCA_OVN_VER}"
            ;;
        *)
            echo "Unknown OVN_SOURCE: ${OVN_SOURCE}" >&2
            exit 1
            ;;
    esac
}

case "${OVS_SOURCE}" in
    artifactory|uca|doca)
        ;;
    *)
        echo "Unknown OVS_SOURCE: ${OVS_SOURCE}" >&2
        exit 1
        ;;
esac
case "${OVN_SOURCE}" in
    artifactory|uca)
        ;;
    *)
        echo "Unknown OVN_SOURCE: ${OVN_SOURCE}" >&2
        exit 1
        ;;
esac

if [ "${OVS_SOURCE}" = artifactory ]; then
    : "${OVS_VER:?OVS_VER is required for Artifactory OVS}"
fi
if [ "${OVN_SOURCE}" = artifactory ]; then
    : "${OVN_VER:?OVN_VER is required for Artifactory OVN}"
fi

apt-get update
apt-get install -y --no-install-recommends ca-certificates curl

if [ "${OVS_SOURCE}" = artifactory ] || [ "${OVN_SOURCE}" = artifactory ]; then
    add_artifactory_repo
fi
if [ "${OVS_SOURCE}" = uca ] || [ "${OVN_SOURCE}" = uca ]; then
    add_uca_repo
fi
if [ "${OVS_SOURCE}" = doca ]; then
    add_doca_repo
fi

apt-get update
install_ovs
install_ovn

rm -f /etc/apt/sources.list.d/ngn-sdn-ovn.list /usr/share/keyrings/nvidia-artifactory.asc
rm -rf /var/lib/apt/lists/*
