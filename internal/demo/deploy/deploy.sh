#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Deploys the demo telemetry generator as an AWS Lambda function triggered every
# 20 minutes by an EventBridge (CloudWatch Events) rule. Idempotent: re-running
# updates the existing function/rule in place.
#
# Run from your machine with AWS credentials configured (env vars, profile, or
# SSO). Required app config:
#
#   DASH0_OTLP_URL    Dash0 OTLP ingress URL          (required)
#   DASH0_AUTH_TOKEN  Dash0 auth token                (required)
#   DASH0_DATASET     Dash0 dataset                   (optional; unset = derived from token)
#   DEMO_TURNS        turns sent per invocation        (default: 1)
#
# Optional infra overrides:
#
#   AWS_REGION        target region                    (default: eu-west-1)
#   FUNCTION_NAME     Lambda name                      (default: dash0-demo-telemetry)
#   SCHEDULE          EventBridge schedule expression  (default: rate(20 minutes))
#   ARCH              arm64 | x86_64                   (default: arm64)
#
# Usage:
#   DASH0_OTLP_URL=... DASH0_AUTH_TOKEN=... ./internal/demo/deploy/deploy.sh
set -euo pipefail

# --- config -----------------------------------------------------------------
FUNCTION_NAME="${FUNCTION_NAME:-dash0-demo-telemetry}"
REGION="${AWS_REGION:-eu-west-1}"
ROLE_NAME="${ROLE_NAME:-${FUNCTION_NAME}-role}"
RULE_NAME="${RULE_NAME:-${FUNCTION_NAME}-schedule}"
SCHEDULE="${SCHEDULE:-rate(20 minutes)}"
RUNTIME="provided.al2023"
ARCH="${ARCH:-arm64}"
TIMEOUT="${TIMEOUT:-30}"

DASH0_DATASET="${DASH0_DATASET:-}"
DEMO_TURNS="${DEMO_TURNS:-1}"
: "${DASH0_OTLP_URL:?set DASH0_OTLP_URL}"
: "${DASH0_AUTH_TOKEN:?set DASH0_AUTH_TOKEN}"

case "$ARCH" in
  arm64)  GOARCH=arm64 ;;
  x86_64) GOARCH=amd64 ;;
  *) echo "ARCH must be arm64 or x86_64, got '$ARCH'" >&2; exit 1 ;;
esac

# Resolve repo root from this script's location so it runs from anywhere.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT

echo "==> Building bootstrap (linux/$GOARCH)"
( cd "$REPO_ROOT" && GOOS=linux GOARCH="$GOARCH" CGO_ENABLED=0 \
    go build -ldflags="-s -w" -o "$BUILD_DIR/bootstrap" ./cmd/demo )
( cd "$BUILD_DIR" && zip -q -j function.zip bootstrap )

ACCOUNT_ID="$(aws sts get-caller-identity --query Account --output text)"
echo "==> Account $ACCOUNT_ID, region $REGION"

# --- IAM execution role ------------------------------------------------------
if ! aws iam get-role --role-name "$ROLE_NAME" >/dev/null 2>&1; then
  echo "==> Creating IAM role $ROLE_NAME"
  aws iam create-role --role-name "$ROLE_NAME" \
    --assume-role-policy-document '{
      "Version": "2012-10-17",
      "Statement": [{
        "Effect": "Allow",
        "Principal": {"Service": "lambda.amazonaws.com"},
        "Action": "sts:AssumeRole"
      }]
    }' >/dev/null
  aws iam attach-role-policy --role-name "$ROLE_NAME" \
    --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole
  echo "    waiting for role propagation..."
  sleep 10
fi
ROLE_ARN="$(aws iam get-role --role-name "$ROLE_NAME" --query Role.Arn --output text)"

# --- Lambda function ---------------------------------------------------------
# Only include DASH0_DATASET when set; otherwise the ingest derives the dataset
# from the auth token (updating the env replaces it, dropping any prior value).
ENV_PAIRS="DASH0_OTLP_URL=$DASH0_OTLP_URL,DASH0_AUTH_TOKEN=$DASH0_AUTH_TOKEN,DEMO_TURNS=$DEMO_TURNS"
if [ -n "$DASH0_DATASET" ]; then
  ENV_PAIRS="$ENV_PAIRS,DASH0_DATASET=$DASH0_DATASET"
fi
ENV_VARS="Variables={$ENV_PAIRS}"

if aws lambda get-function --function-name "$FUNCTION_NAME" --region "$REGION" >/dev/null 2>&1; then
  echo "==> Updating function code"
  aws lambda update-function-code --region "$REGION" \
    --function-name "$FUNCTION_NAME" --zip-file "fileb://$BUILD_DIR/function.zip" >/dev/null
  aws lambda wait function-updated --region "$REGION" --function-name "$FUNCTION_NAME"
  echo "==> Updating function configuration"
  aws lambda update-function-configuration --region "$REGION" \
    --function-name "$FUNCTION_NAME" --runtime "$RUNTIME" --handler bootstrap \
    --timeout "$TIMEOUT" --environment "$ENV_VARS" >/dev/null
else
  echo "==> Creating function $FUNCTION_NAME"
  aws lambda create-function --region "$REGION" \
    --function-name "$FUNCTION_NAME" --runtime "$RUNTIME" --architectures "$ARCH" \
    --handler bootstrap --role "$ROLE_ARN" \
    --zip-file "fileb://$BUILD_DIR/function.zip" --timeout "$TIMEOUT" \
    --environment "$ENV_VARS" >/dev/null
fi
aws lambda wait function-updated --region "$REGION" --function-name "$FUNCTION_NAME"
FUNCTION_ARN="$(aws lambda get-function --region "$REGION" \
  --function-name "$FUNCTION_NAME" --query Configuration.FunctionArn --output text)"

# --- EventBridge schedule ----------------------------------------------------
echo "==> Configuring schedule: $SCHEDULE"
aws events put-rule --region "$REGION" \
  --name "$RULE_NAME" --schedule-expression "$SCHEDULE" \
  --description "Triggers $FUNCTION_NAME on a fixed schedule" >/dev/null
RULE_ARN="$(aws events describe-rule --region "$REGION" --name "$RULE_NAME" --query Arn --output text)"

# Allow EventBridge to invoke the function (idempotent).
aws lambda add-permission --region "$REGION" \
  --function-name "$FUNCTION_NAME" --statement-id "${RULE_NAME}-invoke" \
  --action lambda:InvokeFunction --principal events.amazonaws.com \
  --source-arn "$RULE_ARN" >/dev/null 2>&1 || true

aws events put-targets --region "$REGION" \
  --rule "$RULE_NAME" --targets "Id=1,Arn=$FUNCTION_ARN" >/dev/null

echo "==> Done."
echo "    Function: $FUNCTION_ARN"
echo "    Schedule: $SCHEDULE (rule $RULE_NAME)"
echo
echo "Invoke once now to verify:"
echo "  aws lambda invoke --region $REGION --function-name $FUNCTION_NAME /dev/stdout"
