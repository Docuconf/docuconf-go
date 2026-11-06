// Command orders-go-containerapps deploys the orders service to Azure
// Container Apps with Pulumi: the same inputs as ../containerapps
// (Terraform), set through the component docuconf pulumi -lang go -target
// containerapps generates from the contract (./ordersapiconfig). The
// component checks every input before anything is planned, with the Go
// SDK, the engine the service runs at boot (README.md).
//
// A Container Apps secret holds one file, and a tls input is a directory,
// so the component has no serving-tls input: Container Apps ingress ends
// TLS in front of the app, which serves HTTP.
package main

import (
	app "github.com/pulumi/pulumi-azure-native-sdk/app/v3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"

	"orders-go-containerapps/ordersapiconfig"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		cfg := config.New(ctx, "")
		logLevel := "debug"
		if cfg.Require("environment") == "prod" {
			logLevel = "warn"
		}
		vault := cfg.Require("keyVault")
		// The identity that reads the Key Vault secrets. It must be allowed
		// to read each (the Key Vault Secrets User role) before the first
		// revision starts, which is why it is user-assigned.
		identity := pulumi.String(cfg.Require("identityId"))
		orders, err := ordersapiconfig.NewOrdersApiConfig(ctx, "orders", &ordersapiconfig.Args{
			// Secrets are references, never values: Container Apps reads them
			// from Key Vault. A versionless ID follows the latest version.
			DATABASE_URL: ordersapiconfig.Secret(pulumi.String(vault + "/secrets/orders-db")).WithIdentity(identity),
			WEBHOOK_KEYS: ordersapiconfig.Secret(pulumi.String(vault + "/secrets/orders-webhook-keys")).WithIdentity(identity), // "old,new" while rotating

			LOG_LEVEL:       ordersapiconfig.String(pulumi.String(logLevel)),
			ALLOWED_ORIGINS: ordersapiconfig.Strings(pulumi.StringArray{pulumi.String("https://shop.example.com")}),
			WORKER_COUNT:    ordersapiconfig.Int(pulumi.Int(cfg.RequireInt("workerCount"))),

			// A file input comes from a Key Vault secret, mounted as one file.
			Discounts: ordersapiconfig.SecretFile(pulumi.String(vault + "/secrets/orders-discounts")).WithIdentity(identity),
		})
		if err != nil {
			return err
		}

		_, err = app.NewContainerApp(ctx, "orders", &app.ContainerAppArgs{
			ContainerAppName:     pulumi.String("orders-api"),
			ResourceGroupName:    pulumi.String(cfg.Require("resourceGroup")),
			ManagedEnvironmentId: pulumi.String(cfg.Require("environmentId")),
			Identity: &app.ManagedServiceIdentityArgs{
				Type:                   pulumi.String("UserAssigned"),
				UserAssignedIdentities: pulumi.StringArray{identity},
			},
			Configuration: &app.ConfigurationArgs{
				Secrets: orders.ContainerAppSecrets,
				Ingress: &app.IngressArgs{External: pulumi.Bool(true), TargetPort: pulumi.Int(8080)},
			},
			Template: &app.TemplateArgs{
				Containers: app.ContainerArray{&app.ContainerArgs{
					Name:         pulumi.String("orders-api"),
					Image:        pulumi.String(cfg.Require("image")),
					Env:          orders.ContainerAppEnv,
					VolumeMounts: orders.ContainerAppVolumeMounts,
				}},
				Volumes: orders.ContainerAppVolumes,
			},
		})
		return err
	})
}
