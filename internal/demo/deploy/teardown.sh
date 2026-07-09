#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Removes everything deploy.sh created: the EventBridge rule + target, the
# Lambda function, and the IAM role. Safe to run repeatedly.
#
#   AWS_REGION / FUNCTION_NAME / ROLE_NAME / RULE_NAME override the defaults
#   (must match what was used for deploy.sh).
set -euo pipefail

FUNCTION_NAME="${FUNCTION_NAME:-dash0-demo-telemetry}"
REGION="${AWS_REGION:-eu-west-1}"
ROLE_NAME="${ROLE_NAME:-${FUNCTION_NAME}-role}"
RULE_NAME="${RULE_NAME:-${FUNCTION_NAME}-schedule}"

echo "==> Removing EventBridge target + rule $RULE_NAME"
aws events remove-targets --region "$REGION" --rule "$RULE_NAME" --ids 1 >/dev/null 2>&1 || true
aws events delete-rule --region "$REGION" --name "$RULE_NAME" >/dev/null 2>&1 || true

echo "==> Deleting function $FUNCTION_NAME"
aws lambda delete-function --region "$REGION" --function-name "$FUNCTION_NAME" >/dev/null 2>&1 || true

echo "==> Detaching policy + deleting role $ROLE_NAME"
aws iam detach-role-policy --role-name "$ROLE_NAME" \
  --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole >/dev/null 2>&1 || true
aws iam delete-role --role-name "$ROLE_NAME" >/dev/null 2>&1 || true

echo "==> Done."
