#!/usr/bin/env nod

import "source-map-support/register";
import aws = require("aws-sdk");

const findCfnOutputValue = async (cloudFormation: aws.CloudFormation, key: string): Promise<string> => {
  const stacksRes = await cloudFormation.describeStacks({
    StackName: "NotifyBoothUpdateInfraStack"
  }).promise();
  const stacks = stacksRes.Stacks || [];
  const outputs = stacks[0].Outputs || [];
  const output = outputs.find(o => o.OutputKey === key);
  return output?.OutputValue || "";
};

const encryptKMS = async (kms: aws.KMS, keyId: string, plaintext: string): Promise<string> => {
  const encryptRes = await kms.encrypt({
    KeyId: keyId,
    Plaintext: plaintext
  }).promise();
  const cipherTextBlob = encryptRes.CiphertextBlob;
  if (cipherTextBlob instanceof Buffer) {
    return cipherTextBlob.toString("base64");
  }
  if (cipherTextBlob instanceof String) {
    return cipherTextBlob.toString();
  }
  throw new Error("unknown cipher text type");
};

// add encrypted environment variables into lambda
const main = async (): Promise<void> => {
  aws.config.getCredentials((err: aws.AWSError) => {
    if (err) throw new Error(err.stack);
  });
  aws.config.region = "ap-northeast-1";

  const cloudFormation = new aws.CloudFormation();
  const keyId = await findCfnOutputValue(cloudFormation, "KeyId");
  const functionName = await findCfnOutputValue(cloudFormation, "FunctionName");

  const kms = new aws.KMS();
  const slackUrlCipherText = await encryptKMS(kms, keyId, process.env["SLACK_URL"] || "");
  const slackChannelCipherText = await encryptKMS(kms, keyId, process.env["SLACK_CHANNEL"] || "");

  const lambda = new aws.Lambda();
  const fn = await lambda.getFunction({
    FunctionName: functionName,
  }).promise();
  const envVars = fn.Configuration?.Environment?.Variables || {};

  envVars["ENCRYPTED_SLACK_URL"] = slackUrlCipherText;
  envVars["ENCRYPTED_SLACK_CHANNEL"] = slackChannelCipherText;

  await lambda.updateFunctionConfiguration({
    FunctionName: functionName,
    Environment: {
      Variables: envVars,
    }
  }).promise()
};


main();
