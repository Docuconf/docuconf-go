// Production rules the platform team owns. They are unified with every
// service's values, so they apply to apps that never heard of them.
package policy

// No debug logging in production.
LOG_LEVEL?: "info" | "warn" | "error"

// Trace at most 10% of requests.
TRACE_SAMPLE_RATIO?: <=0.1
