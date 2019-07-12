#!/usr/bin/env bash

# Script to install Nimbess CNI on a Kubernetes host.
# - Expects the host CNI binary path to be mounted at /host/opt/cni/bin.
# - Expects the host CNI network config path to be mounted at /host/etc/cni/net.d.
# - Expects the desired CNI config in the CNI_NETWORK_CONFIG env variable.

# Ensure all variables are defined, and that the script fails when an error is hit.
set -u -e

# Capture the usual signals and exit from the script
trap 'echo "INT received, simply exiting..."; exit 0' INT
trap 'echo "TERM received, simply exiting..."; exit 0' TERM
trap 'echo "HUP received, simply exiting..."; exit 0' HUP

# Helper function for raising errors
# Usage:
# some_command || exit_with_error "some_command_failed: maybe try..."
exit_with_error(){
  echo "$1"
  exit 1
}

# The directory on the host where CNI networks are installed. Defaults to
# /etc/cni/net.d, but can be overridden by setting CNI_NET_DIR.  This is used
# for populating absolute paths in the CNI network config to assets
# which are installed in the CNI network config directory.
HOST_CNI_NET_DIR=${CNI_NET_DIR:-/etc/cni/net.d}

# Clean up any existing binaries / config / assets.
rm -f /host/opt/cni/bin/nimbess-cni

# Choose which default cni binaries should be copied
SKIP_CNI_BINARIES=${SKIP_CNI_BINARIES:-""}
SKIP_CNI_BINARIES=",$SKIP_CNI_BINARIES,"
UPDATE_CNI_BINARIES=${UPDATE_CNI_BINARIES:-"true"}

# Place the new binaries if the directory is writeable.
for dir in /host/opt/cni/bin /host/secondary-bin-dir
do
  if [ ! -w "$dir" ];
  then
    echo "$dir is non-writeable, skipping"
    continue
  fi
  for path in /opt/cni/bin/*;
  do
    filename="$(basename "$path")"
    tmp=",$filename,"
    if [ "${SKIP_CNI_BINARIES#*$tmp}" != "$SKIP_CNI_BINARIES" ];
    then
      echo "$filename is in SKIP_CNI_BINARIES, skipping"
      continue
    fi
    if [ "${UPDATE_CNI_BINARIES}" != "true" ] && [ -f $dir/"$filename" ];
    then
      echo "$dir/$filename is already here and UPDATE_CNI_BINARIES isn't true, skipping"
      continue
    fi
    cp "$path" $dir/ || exit_with_error "Failed to copy $path to $dir. This may be caused by selinux configuration on the host, or something else."
  done

  echo "Wrote Nimbess CNI binaries to $dir"
done

TMP_CONF='/nimbess.conf.tmp'
# If specified, overwrite the network configuration file.
: "${CNI_NETWORK_CONFIG_FILE:=}"
: "${CNI_NETWORK_CONFIG:=}"
if [ -e "${CNI_NETWORK_CONFIG_FILE}" ]; then
  echo "Using CNI config template from ${CNI_NETWORK_CONFIG_FILE}."
  cp "${CNI_NETWORK_CONFIG_FILE}" "${TMP_CONF}"
elif [ "${CNI_NETWORK_CONFIG}" != "" ]; then
  echo "Using CNI config template from CNI_NETWORK_CONFIG environment variable."
  cat >$TMP_CONF <<EOF
${CNI_NETWORK_CONFIG}
EOF
fi

CNI_CONF_NAME=${CNI_CONF_NAME:-10-nimbess.conf}
CNI_OLD_CONF_NAME=${CNI_OLD_CONF_NAME:-10-nimbess.conf}

# Log the config file before inserting service account token.
# This way auth token is not visible in the logs.
echo "CNI config: $(cat ${TMP_CONF})"

# Move the temporary CNI config into place.
mv "$TMP_CONF" /host/etc/cni/net.d/"${CNI_CONF_NAME}" || \
  exit_with_error "Failed to mv files. This may be caused by selinux configuration on the host, or something else."

echo "Created CNI config ${CNI_CONF_NAME}"

# Unless told otherwise, sleep forever.
# This prevents Kubernetes from restarting the pod repeatedly.
should_sleep=${SLEEP:-"true"}
echo "Done configuring CNI.  Sleep=$should_sleep"
while [ "$should_sleep" = "true"  ]; do
    sleep 10
done
