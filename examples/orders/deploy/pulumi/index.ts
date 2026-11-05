// The orders service on Kubernetes, from Pulumi: the same inputs as
// ../values.yaml and ../files.yaml, set through the component docuconf
// pulumi generates from the contract (./orders-config). The component
// checks every input before anything is rendered, with @docuconf/core,
// the engine the service runs at boot (README.md).

import * as k8s from "@pulumi/kubernetes";
import * as pulumi from "@pulumi/pulumi";

import { OrdersApiConfig } from "./orders-config";

const config = new pulumi.Config();
const namespace = config.require("namespace");
const image = config.require("image");
const environment = config.require("environment");

// With renderDir set, the provider writes the manifests there instead of
// applying them, so the program runs without a cluster (CI does this).
const renderDir = config.get("renderDir");
const provider = new k8s.Provider("k8s", renderDir ? { renderYamlToDirectory: renderDir } : {});

const orders = new OrdersApiConfig("orders", {
  // Secrets are references, never values: the arguments have no value for them.
  DATABASE_URL: { secretKeyRef: { name: "orders-db", key: "url" } },
  WEBHOOK_KEYS: { secretKeyRef: { name: "orders-webhooks", key: "keys" } }, // "old,new" while rotating

  LOG_LEVEL: { value: environment === "prod" ? "warn" : "debug" },
  ALLOWED_ORIGINS: { value: ["https://shop.example.com"] },
  WORKER_COUNT: { value: config.requireNumber("workerCount") },

  // File inputs: a cert-manager Certificate, and the discount codes inline.
  "serving-tls": {
    certificate: {
      name: "orders-tls",
      secretName: "orders-tls",
      dnsNames: ["orders.example.com"],
      duration: "2160h",
      renewBefore: "720h",
    },
  },
  discounts: { inline: { codes: { WELCOME10: 10, SPRING25: 25 } } },
});

// The component renders the ConfigMaps; the program declares them.
for (const cm of orders.kubernetesConfigMaps) {
  new k8s.core.v1.ConfigMap(cm.name, { metadata: { name: cm.name, namespace }, data: cm.data, immutable: cm.immutable }, { provider });
}

new k8s.apps.v1.Deployment(
  "orders-api",
  {
    metadata: { name: "orders-api", namespace },
    spec: {
      selector: { matchLabels: { app: "orders-api" } },
      template: {
        // On the pod template, never the Deployment: injectors read the pod's.
        metadata: { labels: { app: "orders-api" }, annotations: orders.podAnnotations },
        spec: {
          // The image runs as uid 65532; fsGroup makes the TLS Secret, mounted 0400, readable to it.
          securityContext: { runAsNonRoot: true, runAsUser: 65532, fsGroup: 65532 },
          containers: [
            {
              name: "orders-api",
              image,
              env: orders.kubernetesEnv,
              volumeMounts: orders.kubernetesVolumeMounts,
              securityContext: { allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: { drop: ["ALL"] } },
            },
          ],
          volumes: orders.kubernetesVolumes,
        },
      },
    },
  },
  { provider },
);
