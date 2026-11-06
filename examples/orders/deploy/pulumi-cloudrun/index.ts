// The orders service on Cloud Run, from Pulumi: the same inputs as
// ../cloudrun (Terraform), set through the component docuconf pulumi
// -target cloudrun generates from the contract (./orders-config). The
// component checks every input before anything is planned, with
// @docuconf/core, the engine the service runs at boot (README.md).
//
// A Cloud Run secret volume holds one file, and a tls input is a directory,
// so the component has no serving-tls input: Cloud Run ends TLS in front of
// the service, which serves HTTP.

import * as gcp from "@pulumi/gcp";
import * as pulumi from "@pulumi/pulumi";

import { OrdersApiConfig } from "./orders-config";

const config = new pulumi.Config();
const environment = config.require("environment");

const orders = new OrdersApiConfig("orders", {
  // Secrets are references, never values: Cloud Run reads them when it starts an instance.
  DATABASE_URL: { secret: { secret: "orders-db" } },
  WEBHOOK_KEYS: { secret: { secret: "orders-webhook-keys" } }, // "old,new" while rotating

  LOG_LEVEL: { value: environment === "prod" ? "warn" : "debug" },
  ALLOWED_ORIGINS: { value: ["https://shop.example.com"] },
  WORKER_COUNT: { value: config.requireNumber("workerCount") },

  // A file input comes from a Secret Manager secret, mounted as one file.
  discounts: { secret: { secret: "orders-discounts" } },
});

new gcp.cloudrunv2.Service("orders", {
  name: "orders-api",
  location: new pulumi.Config("gcp").require("region"),
  template: {
    serviceAccount: config.require("serviceAccount"),
    containers: [
      {
        image: config.require("image"),
        ports: { containerPort: 8080 },
        envs: orders.cloudrunEnv,
        volumeMounts: orders.cloudrunVolumeMounts,
      },
    ],
    volumes: orders.cloudrunVolumes,
  },
});
