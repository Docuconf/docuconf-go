// Command orders-go deploys the orders service to Kubernetes with Pulumi:
// the same inputs as ../values.yaml and ../files.yaml, set through the
// component docuconf pulumi -lang go generates from the contract
// (./ordersapiconfig). The component checks every input before anything
// is rendered, with the Go SDK, the engine the service runs at boot
// (README.md).
package main

import (
	"github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"

	"orders-go/ordersapiconfig"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		cfg := config.New(ctx, "")
		namespace := cfg.Require("namespace")
		image := cfg.Require("image")
		environment := cfg.Require("environment")
		// The discount codes, written into an immutable, content-hashed
		// ConfigMap: changing them rolls the Deployment.
		codes := map[string]any{"WELCOME10": 10, "SPRING25": 25}
		if cfg.Get("discountCodes") != "" {
			codes = map[string]any{}
			if err := cfg.TryObject("discountCodes", &codes); err != nil {
				return err
			}
		}

		// With renderDir set, the provider writes the manifests there
		// instead of applying them, so the program runs without a cluster
		// (CI does this).
		providerArgs := &kubernetes.ProviderArgs{}
		if dir := cfg.Get("renderDir"); dir != "" {
			providerArgs.RenderYamlToDirectory = pulumi.String(dir)
		}
		provider, err := kubernetes.NewProvider(ctx, "k8s", providerArgs)
		if err != nil {
			return err
		}

		logLevel := "debug"
		if environment == "prod" {
			logLevel = "warn"
		}
		orders, err := ordersapiconfig.NewOrdersApiConfig(ctx, "orders", &ordersapiconfig.Args{
			// Secrets are references, never values: a SecretVar has no literal.
			DATABASE_URL: ordersapiconfig.SecretKeyRef(pulumi.String("orders-db"), pulumi.String("url")),
			WEBHOOK_KEYS: ordersapiconfig.SecretKeyRef(pulumi.String("orders-webhooks"), pulumi.String("keys")), // "old,new" while rotating

			LOG_LEVEL:       ordersapiconfig.String(pulumi.String(logLevel)),
			ALLOWED_ORIGINS: ordersapiconfig.Strings(pulumi.StringArray{pulumi.String("https://shop.example.com")}),
			WORKER_COUNT:    ordersapiconfig.Int(pulumi.Int(cfg.RequireInt("workerCount"))),

			// File inputs: a cert-manager Certificate, and the discount codes inline.
			ServingTLS: ordersapiconfig.Certificate(ordersapiconfig.CertificateSpec{
				Name:        "orders-tls",
				SecretName:  pulumi.String("orders-tls"),
				DNSNames:    []string{"orders.example.com"},
				Duration:    "2160h",
				RenewBefore: "720h",
			}),
			Discounts: ordersapiconfig.Inline(map[string]any{"codes": codes}),
		})
		if err != nil {
			return err
		}

		// The component renders the ConfigMaps; the program declares them.
		for _, cm := range orders.KubernetesConfigMaps {
			if _, err := corev1.NewConfigMap(ctx, cm.Name, &corev1.ConfigMapArgs{
				Metadata:  &metav1.ObjectMetaArgs{Name: pulumi.String(cm.Name), Namespace: pulumi.String(namespace)},
				Data:      pulumi.ToStringMap(cm.Data),
				Immutable: pulumi.Bool(cm.Immutable),
			}, pulumi.Provider(provider)); err != nil {
				return err
			}
		}

		labels := pulumi.StringMap{"app": pulumi.String("orders-api")}
		_, err = appsv1.NewDeployment(ctx, "orders-api", &appsv1.DeploymentArgs{
			Metadata: &metav1.ObjectMetaArgs{Name: pulumi.String("orders-api"), Namespace: pulumi.String(namespace)},
			Spec: &appsv1.DeploymentSpecArgs{
				Selector: &metav1.LabelSelectorArgs{MatchLabels: labels},
				Template: &corev1.PodTemplateSpecArgs{
					// On the pod template, never the Deployment: injectors read the pod's.
					Metadata: &metav1.ObjectMetaArgs{Labels: labels, Annotations: orders.PodAnnotations},
					Spec: &corev1.PodSpecArgs{
						// The image runs as uid 65532; fsGroup makes the TLS
						// Secret, mounted 0400, readable to it.
						SecurityContext: &corev1.PodSecurityContextArgs{RunAsNonRoot: pulumi.Bool(true), RunAsUser: pulumi.Int(65532), FsGroup: pulumi.Int(65532)},
						Containers: corev1.ContainerArray{&corev1.ContainerArgs{
							Name:         pulumi.String("orders-api"),
							Image:        pulumi.String(image),
							Env:          orders.KubernetesEnv,
							VolumeMounts: orders.KubernetesVolumeMounts,
							SecurityContext: &corev1.SecurityContextArgs{
								AllowPrivilegeEscalation: pulumi.Bool(false),
								ReadOnlyRootFilesystem:   pulumi.Bool(true),
								Capabilities:             &corev1.CapabilitiesArgs{Drop: pulumi.StringArray{pulumi.String("ALL")}},
							},
						}},
						Volumes: orders.KubernetesVolumes,
					},
				},
			},
		}, pulumi.Provider(provider))
		return err
	})
}
