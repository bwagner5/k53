#!/usr/bin/env bash

if [ -z "${CLUSTER_NAME}" ]; then
	echo CLUSTER_NAME environment variable is not set
	exit 1
fi

if [ -z "${AWS_ACCOUNT_ID}" ]; then
	echo AWS_ACCOUNT_ID environment variable is not set
	exit 1
fi

aws cloudformation deploy \
  --stack-name "k53-${CLUSTER_NAME}" \
  --template-file scripts/cloudformation.yaml \
  --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides "ClusterName=${CLUSTER_NAME}"

eksctl create iamserviceaccount \
  --cluster "${CLUSTER_NAME}" \
  --name k53 \
  --namespace k53 \
  --role-name "${CLUSTER_NAME}-k53" \
  --attach-policy-arn "arn:aws:iam::${AWS_ACCOUNT_ID}:policy/K53ControllerPolicy-${CLUSTER_NAME}" \
  --role-only \
  --approve
