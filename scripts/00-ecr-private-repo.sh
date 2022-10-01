#!/usr/bin/env bash
set -euo pipefail

if [ -z "${AWS_REGION}" ]; then
	echo AWS_REGION environment variable is not set
	exit 1
fi

aws ecr create-repository \
    --repository-name k53/controller \
    --image-scanning-configuration scanOnPush=true \
    --region "${AWS_REGION}" || :