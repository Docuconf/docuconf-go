package lint

import (
	"reflect"
	"testing"
)

func TestParseValueFrom(t *testing.T) {
	for in, want := range map[string]map[string]any{
		"arn:aws:secretsmanager:us-east-1:123456789012:secret:orders/prod/db-AbCdEf": {"awsSecretsManager": map[string]any{
			"arn": "arn:aws:secretsmanager:us-east-1:123456789012:secret:orders/prod/db-AbCdEf", "partition": "aws", "region": "us-east-1", "account": "123456789012", "name": "orders/prod/db",
		}},
		"arn:aws-us-gov:secretsmanager:us-gov-west-1:123456789012:secret:db-AbCdEf:password::v1": {"awsSecretsManager": map[string]any{
			"arn": "arn:aws-us-gov:secretsmanager:us-gov-west-1:123456789012:secret:db-AbCdEf", "partition": "aws-us-gov", "region": "us-gov-west-1", "account": "123456789012", "name": "db", "jsonKey": "password", "versionId": "v1",
		}},
		"arn:aws:ssm:eu-west-1:123456789012:parameter/orders/db": {"awsSsmParameter": map[string]any{
			"arn": "arn:aws:ssm:eu-west-1:123456789012:parameter/orders/db", "partition": "aws", "region": "eu-west-1", "account": "123456789012", "name": "/orders/db",
		}},
		"orders-db": {"awsSsmParameter": map[string]any{"name": "orders-db"}},
	} {
		if got := ParseValueFrom(in); !reflect.DeepEqual(got, want) {
			t.Errorf("ParseValueFrom(%s)\n got %v\nwant %v", in, got, want)
		}
	}
}

func TestParseGCPSecret(t *testing.T) {
	if got, want := ParseGCPSecret("projects/acme/secrets/db", "4"), map[string]any{"gcpSecretManager": map[string]any{"project": "acme", "secret": "db", "version": "4"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got, want := ParseGCPSecret("db", "latest"), map[string]any{"gcpSecretManager": map[string]any{"secret": "db", "version": "latest"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
