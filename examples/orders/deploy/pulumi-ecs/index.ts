// The orders service on Amazon ECS (Fargate), from Pulumi: the same inputs
// as ../ecs (Terraform), set through the component docuconf pulumi -target
// ecs generates from the contract (./orders-config). The component checks
// every input before anything is planned, with @docuconf/core, the engine
// the service runs at boot (README.md).
//
// ECS mounts no files, so the component has no discounts or serving-tls
// inputs: the service runs without discount codes and serves HTTP, with TLS
// ended at the load balancer.

import * as aws from "@pulumi/aws";
import * as pulumi from "@pulumi/pulumi";

import { OrdersApiConfig } from "./orders-config";

const config = new pulumi.Config();
const environment = config.require("environment");

const orders = new OrdersApiConfig("orders", {
  // Secrets are references, never values: ECS reads them when it starts the task.
  DATABASE_URL: { secret: { valueFrom: config.require("databaseUrlSecretArn"), jsonKey: "url" } },
  WEBHOOK_KEYS: { secret: { valueFrom: config.require("webhookKeysParameterArn") } }, // "old,new" while rotating

  LOG_LEVEL: { value: environment === "prod" ? "warn" : "debug" },
  ALLOWED_ORIGINS: { value: ["https://shop.example.com"] },
  WORKER_COUNT: { value: config.requireNumber("workerCount") },
});

new aws.ecs.TaskDefinition("orders", {
  family: "orders-api",
  requiresCompatibilities: ["FARGATE"],
  networkMode: "awsvpc",
  cpu: "256",
  memory: "512",
  executionRoleArn: config.require("executionRoleArn"),
  containerDefinitions: pulumi.jsonStringify([
    {
      name: "orders-api",
      image: config.require("image"),
      essential: true,
      user: "65532",
      readonlyRootFilesystem: true,
      portMappings: [{ containerPort: 8080, protocol: "tcp" }],
      environment: orders.ecsEnvironment, // [{ name, value }]
      secrets: orders.ecsSecrets, // [{ name, valueFrom }]
    },
  ]),
});
