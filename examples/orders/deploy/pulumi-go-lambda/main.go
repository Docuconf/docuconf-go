// Command orders-go-lambda deploys the orders service to AWS Lambda with
// Pulumi: the same inputs as ../lambda (Terraform), set through the
// component docuconf pulumi -lang go -target lambda generates from the
// contract (./ordersapiconfig). The component checks every input before
// anything is planned, with the Go SDK, the engine the service runs at
// boot (README.md).
//
// Lambda mounts no files, so the component has no discounts or serving-tls
// inputs, and it resolves no secret references in its environment: the
// secrets are injected as their ARNs, and the function reads them through
// the AWS Parameters and Secrets Lambda Extension.
package main

import (
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"

	"orders-go-lambda/ordersapiconfig"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		cfg := config.New(ctx, "")
		logLevel := "debug"
		if cfg.Require("environment") == "prod" {
			logLevel = "warn"
		}
		orders, err := ordersapiconfig.NewOrdersApiConfig(ctx, "orders", &ordersapiconfig.Args{
			// Secrets are injected as their ARNs: the function reads them at start.
			DATABASE_URL: ordersapiconfig.Injected("aws-parameters-and-secrets").WithRef(pulumi.String(cfg.Require("databaseUrlSecretArn"))),
			WEBHOOK_KEYS: ordersapiconfig.Injected("aws-parameters-and-secrets").WithRef(pulumi.String(cfg.Require("webhookKeysParameterArn"))),

			LOG_LEVEL:       ordersapiconfig.String(pulumi.String(logLevel)),
			ALLOWED_ORIGINS: ordersapiconfig.Strings(pulumi.StringArray{pulumi.String("https://shop.example.com")}),
			WORKER_COUNT:    ordersapiconfig.Int(pulumi.Int(cfg.RequireInt("workerCount"))),
		})
		if err != nil {
			return err
		}

		_, err = lambda.NewFunction(ctx, "orders", &lambda.FunctionArgs{
			Name:        pulumi.String("orders-api"),
			Role:        pulumi.String(cfg.Require("roleArn")),
			PackageType: pulumi.String("Image"),
			ImageUri:    pulumi.String(cfg.Require("image")),
			Timeout:     pulumi.Int(30),
			Environment: &lambda.FunctionEnvironmentArgs{Variables: orders.LambdaEnvironment},
		})
		return err
	})
}
