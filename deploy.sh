set -xe

test -n "${S3_KEY}" || exit 1
test -n "${BOOTH_URL}" || exit 1
test -n "${SLACK_URL}" || exit 1
test -n "${SLACK_CHANNEL}" || exit 1

GOOS=linux GOARCH=amd64 go build -trimpath -o main main.go
mkdir -p infra/lambda/
mv main infra/lambda/

pushd $(pwd)
cd infra
cdk deploy
popd

function_name=$(
  aws cloudformation describe-stacks \
    --stack-name NotifyBoothUpdateInfraStack \
    --query "Stacks[0].Outputs[?OutputKey=='FunctionName'].OutputValue" \
    --output text
)

key_id=$(
  aws cloudformation describe-stacks \
    --stack-name NotifyBoothUpdateInfraStack \
    --query "Stacks[0].Outputs[?OutputKey=='KeyId'].OutputValue" \
    --output text
)

encrypted_slack_url=$(
  aws kms encrypt \
    --key-id $key_id \
    --plaintext fileb://<(echo -n $SLACK_URL) \
    --query "CiphertextBlob" \
    --output text
)

encrypted_slack_channel=$(
  aws kms encrypt \
    --key-id $key_id \
    --plaintext "$SLACK_CHANNEL" \
    --query "CiphertextBlob" \
    --output text
)

aws lambda get-function \
  --function-name $function_name \
  --query 'Configuration.Environment' >current-env.json
echo "{
  \"Variables\": {
    \"ENCRYPTED_SLACK_URL\":\"$encrypted_slack_url\",
    \"ENCRYPTED_SLACK_CHANNEL\":\"$encrypted_slack_channel\"
  }
}" >new-env.json

cat current-env.json | jq
cat new-env.json | jq
lambda_env=$(jq -s '.[0] * .[1]' current-env.json new-env.json)

rm -rf current-env.json new-env.json

aws lambda update-function-configuration \
  --function-name "$function_name" \
  --environment "$lambda_env"
