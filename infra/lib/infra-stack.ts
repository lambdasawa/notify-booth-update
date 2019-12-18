import cdk = require("@aws-cdk/core");
import iam = require("@aws-cdk/aws-iam");
import kms = require("@aws-cdk/aws-kms");
import s3 = require("@aws-cdk/aws-s3");
import lambda = require("@aws-cdk/aws-lambda");
import events = require("@aws-cdk/aws-events");
import eventsTargets = require("@aws-cdk/aws-events-targets");
import logs = require("@aws-cdk/aws-logs");
import { SnsAction } from "@aws-cdk/aws-cloudwatch-actions";
import { SubscriptionProtocol, Topic, Subscription } from "@aws-cdk/aws-sns";
import { TreatMissingData, Alarm, Metric } from "@aws-cdk/aws-cloudwatch";

export class InfraStack extends cdk.Stack {
  private schedule = "3 minutes";

  constructor(scope: cdk.Construct, id: string, props?: cdk.StackProps) {
    super(scope, id, props);

    const account = new iam.AccountPrincipal(this.account);

    const key = new kms.Key(this, "Key", {
      enableKeyRotation: true
    });
    const keyAlias = key.addAlias("alias/notify-booth-update");

    keyAlias.grantEncryptDecrypt(account);

    const bucket = new s3.Bucket(this, "Bucket", {
      removalPolicy: cdk.RemovalPolicy.DESTROY
    });

    const func = new lambda.Function(this, "Function", {
      code: lambda.Code.fromAsset("./lambda"),
      handler: "main",
      runtime: lambda.Runtime.GO_1_X,
      environment: {
        S3_BUCKET: bucket.bucketName,
        S3_KEY: process.env["S3_KEY"] || "",
        BOOTH_URL: process.env["BOOTH_URL"] || "",
        ENCRYPTED_SLACK_URL: "",
        ENCRYPTED_SLACK_CHANNEL: ""
      },
      logRetention: logs.RetentionDays.ONE_WEEK
    });

    const logGroup = new logs.LogGroup(this, "LogGroup", {
      logGroupName: `/aws/lambda/${func.functionName}`
    });
    this.addLogGroupAlarm(logGroup, "notify-booth-update", "error-log");

    keyAlias.grantEncryptDecrypt(func);

    bucket.grantReadWrite(func);

    const rule = new events.Rule(this, "Rule", {
      schedule: events.Schedule.expression(`rate(${this.schedule})`)
    });

    const lambdaEventTarget = new eventsTargets.LambdaFunction(func);

    rule.addTarget(lambdaEventTarget);

    new cdk.CfnOutput(this, "FunctionName", {
      value: func.functionName
    });
    new cdk.CfnOutput(this, "KeyId", {
      value: keyAlias.keyId
    });
  }

  private addLogGroupAlarm(
    logGroup: logs.LogGroup,
    metricNamespace: string,
    metricName: string
  ) {
    logGroup.addMetricFilter("MetricFilter", {
      metricNamespace: metricNamespace,
      metricName: metricName,
      filterPattern: logs.FilterPattern.literal(`{ $.level = "error" }`)
    });

    const topic = new Topic(this, "Topic", {});

    const subscription = new Subscription(this, "Subscription", {
      protocol: SubscriptionProtocol.EMAIL,
      endpoint: process.env["ALERT_EMAIL"] || "",
      topic: topic
    });

    const alarm = new Alarm(this, "Alarm", {
      metric: new Metric({
        metricName: metricName,
        namespace: metricNamespace
      }),
      evaluationPeriods: 1,
      threshold: 1,
      treatMissingData: TreatMissingData.NOT_BREACHING
    });
    alarm.addAlarmAction(new SnsAction(topic));
  }
}
