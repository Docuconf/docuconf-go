// Command orders-go-ecs deploys the orders service to Amazon ECS (Fargate)
// with Pulumi: the same inputs as ../ecs (Terraform), set through the
// component docuconf pulumi -lang go -target ecs generates from the
// contract (./ordersapiconfig). The component checks every input before
// anything is planned, with the Go SDK, the engine the service runs at
// boot (README.md).
//
// ECS mounts no files, so the component has no discounts or serving-tls
// inputs: the service runs without discount codes and serves HTTP, with
// TLS ended at the load balancer.
package main

import (
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ecs"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"

	"orders-go-ecs/ordersapiconfig"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		cfg := config.New(ctx, "")
		logLevel := "debug"
		if cfg.Require("environment") == "prod" {
			logLevel = "warn"
		}
		orders, err := ordersapiconfig.NewOrdersApiConfig(ctx, "orders", &ordersapiconfig.Args{
			// Secrets are references, never values: ECS reads them when it starts the task.
			DATABASE_URL: ordersapiconfig.Secret(pulumi.String(cfg.Require("databaseUrlSecretArn"))).WithJSONKey("url"),
			WEBHOOK_KEYS: ordersapiconfig.Secret(pulumi.String(cfg.Require("webhookKeysParameterArn"))), // "old,new" while rotating

			LOG_LEVEL:       ordersapiconfig.String(pulumi.String(logLevel)),
			ALLOWED_ORIGINS: ordersapiconfig.Strings(pulumi.StringArray{pulumi.String("https://shop.example.com")}),
			WORKER_COUNT:    ordersapiconfig.Int(pulumi.Int(cfg.RequireInt("workerCount"))),
		})
		if err != nil {
			return err
		}

		_, err = ecs.NewTaskDefinition(ctx, "orders", &ecs.TaskDefinitionArgs{
			Family:                  pulumi.String("orders-api"),
			RequiresCompatibilities: pulumi.StringArray{pulumi.String("FARGATE")},
			NetworkMode:             pulumi.String("awsvpc"),
			Cpu:                     pulumi.String("256"),
			Memory:                  pulumi.String("512"),
			ExecutionRoleArn:        pulumi.String(cfg.Require("executionRoleArn")),
			ContainerDefinitions: pulumi.JSONMarshal(pulumi.Array{pulumi.Map{
				"name":                   pulumi.String("orders-api"),
				"image":                  pulumi.String(cfg.Require("image")),
				"essential":              pulumi.Bool(true),
				"user":                   pulumi.String("65532"),
				"readonlyRootFilesystem": pulumi.Bool(true),
				"portMappings":           pulumi.Array{pulumi.Map{"containerPort": pulumi.Int(8080), "protocol": pulumi.String("tcp")}},
				"environment":            orders.EcsEnvironment, // [{ name, value }]
				"secrets":                orders.EcsSecrets,     // [{ name, valueFrom }]
			}}),
		})
		return err
	})
}
