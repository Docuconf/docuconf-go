// Command orders-go-cloudrun deploys the orders service to Cloud Run with
// Pulumi: the same inputs as ../cloudrun (Terraform), set through the
// component docuconf pulumi -lang go -target cloudrun generates from the
// contract (./ordersapiconfig). The component checks every input before
// anything is planned, with the Go SDK, the engine the service runs at
// boot (README.md).
//
// A Cloud Run secret volume holds one file, and a tls input is a
// directory, so the component has no serving-tls input: Cloud Run ends TLS
// in front of the service, which serves HTTP.
package main

import (
	"github.com/pulumi/pulumi-gcp/sdk/v8/go/gcp/cloudrunv2"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"

	"orders-go-cloudrun/ordersapiconfig"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		cfg := config.New(ctx, "")
		logLevel := "debug"
		if cfg.Require("environment") == "prod" {
			logLevel = "warn"
		}
		orders, err := ordersapiconfig.NewOrdersApiConfig(ctx, "orders", &ordersapiconfig.Args{
			// Secrets are references, never values: Cloud Run reads them when it starts an instance.
			DATABASE_URL: ordersapiconfig.Secret(pulumi.String("orders-db")),
			WEBHOOK_KEYS: ordersapiconfig.Secret(pulumi.String("orders-webhook-keys")), // "old,new" while rotating

			LOG_LEVEL:       ordersapiconfig.String(pulumi.String(logLevel)),
			ALLOWED_ORIGINS: ordersapiconfig.Strings(pulumi.StringArray{pulumi.String("https://shop.example.com")}),
			WORKER_COUNT:    ordersapiconfig.Int(pulumi.Int(cfg.RequireInt("workerCount"))),

			// A file input comes from a Secret Manager secret, mounted as one file.
			Discounts: ordersapiconfig.SecretFile(pulumi.String("orders-discounts")),
		})
		if err != nil {
			return err
		}

		_, err = cloudrunv2.NewService(ctx, "orders", &cloudrunv2.ServiceArgs{
			Name:     pulumi.String("orders-api"),
			Location: pulumi.String(config.Require(ctx, "gcp:region")),
			Template: &cloudrunv2.ServiceTemplateArgs{
				ServiceAccount: pulumi.String(cfg.Require("serviceAccount")),
				Containers: cloudrunv2.ServiceTemplateContainerArray{&cloudrunv2.ServiceTemplateContainerArgs{
					Image:        pulumi.String(cfg.Require("image")),
					Ports:        &cloudrunv2.ServiceTemplateContainerPortsArgs{ContainerPort: pulumi.Int(8080)},
					Envs:         orders.CloudRunEnv,
					VolumeMounts: orders.CloudRunVolumeMounts,
				}},
				Volumes: orders.CloudRunVolumes,
			},
		})
		return err
	})
}
