#!/bin/bash

# Copyright 2016 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


###############################################################################
#
# dev-build.sh
#
# Convenience script for developing kops AND nodeup.
#
# This script (by design) will handle building a full kops cluster in AWS,
# with a custom version of the Nodeup binary compiled at runtime.
#
# This script and Makefile uses aws client
# https://aws.amazon.com/cli/
# and make sure you `aws configure`
#
# Example usage
#
# KOPS_STATE_STORE="s3://my-dev-s3-state \
# CLUSTER_NAME="fullcluster.name.mydomain.io" \
# NODEUP_BUCKET="s3-devel-bucket-name-store-nodeup" \
# IMAGE="kope.io/k8s-1.4-debian-jessie-amd64-hvm-ebs-2016-10-21" \
# ./dev-build.sh
#
###############################################################################

KOPS_DIRECTORY="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

#
# Check that required binaries are installed
#
command -v make >/dev/null 2>&1 || { echo >&2 "I require make but it's not installed.  Aborting."; exit 1; }
command -v go >/dev/null 2>&1 || { echo >&2 "I require go but it's not installed.  Aborting."; exit 1; }
command -v docker >/dev/null 2>&1 || { echo >&2 "I require docker but it's not installed.  Aborting."; exit 1; }
command -v aws >/dev/null 2>&1 || { echo >&2 "I require aws but it's not installed.  Aborting."; exit 1; }

#
# Check that expected vars are set
#
[ -z "$KOPS_STATE_STORE" ] && echo "Need to set KOPS_STATE_STORE" && exit 1;
[ -z "$CLUSTER_NAME" ] && echo "Need to set CLUSTER_NAME" && exit 1;
[ -z "$NODEUP_BUCKET" ] && echo "Need to set NODEUP_BUCKET" && exit 1;
[ -z "$IMAGE" ] && echo "Need to set IMAGE or use the image listed here https://github.com/kubernetes/kops/blob/master/channels/stable" && exit 1;

# Cluster config
NODE_COUNT=${NODE_COUNT:-3}
NODE_ZONES=${NODE_ZONES:-"us-west-2a,us-west-2b,us-west-2c"}
NODE_SIZE=${NODE_SIZE:-m4.xlarge}
MASTER_ZONES=${MASTER_ZONES:-"us-west-2a,us-west-2b,us-west-2c"}
MASTER_SIZE=${MASTER_SIZE:-m4.large}

# NETWORK
TOPOLOGY=${TOPOLOGY:-private}
NETWORKING=${NETWORKING:-weave}

# How verbose go logging is
VERBOSITY=${VERBOSITY:-10}

cd $KOPS_DIRECTORY/..

GIT_VER=git-$(git describe --always)
[ -z "$GIT_VER" ] && echo "we do not have GIT_VER something is very wrong" && exit 1;

NODEUP_URL="https://${NODEUP_BUCKET}.s3.amazonaws.com/kops/${GIT_VER}/linux/amd64/nodeup"

echo ==========
echo "Starting build"

make ci && S3_BUCKET=s3://${NODEUP_BUCKET} make upload

echo ==========
echo "Deleting cluster ${CLUSTER_NAME}. Elle est finie."

kops delete cluster \
  --name $CLUSTER_NAME \
  --state $KOPS_STATE_STORE \
  -v $VERBOSITY \
  --yes

echo ==========
echo "Creating cluster ${CLUSTER_NAME}"

NODEUP_URL=${NODEUP_URL} kops create cluster \
  --name $CLUSTER_NAME \
  --state $KOPS_STATE_STORE \
  --node-count $NODE_COUNT \
  --zones $NODE_ZONES \
  --master-zones $MASTER_ZONES \
  --cloud aws \
  --node-size $NODE_SIZE \
  --master-size $MASTER_SIZE \
  --topology $TOPOLOGY \
  --networking $NETWORKING \
  -v $VERBOSITY \
  --image $IMAGE \
  --yes

echo ==========
echo "Your k8s cluster ${CLUSTER_NAME}, awaits your bidding."