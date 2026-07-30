package main

import (
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type composeFile struct {
	Services map[string]composeService `yaml:"services"`
	Volumes  map[string]any            `yaml:"volumes"`
}

type composeService struct {
	Image       string            `yaml:"image"`
	Environment map[string]string `yaml:"environment"`
	DependsOn   map[string]struct {
		Condition string `yaml:"condition"`
	} `yaml:"depends_on"`
	Healthcheck map[string]any `yaml:"healthcheck"`
	Volumes     []string       `yaml:"volumes"`
}

func TestComposeStartsDatabaseAndRedisBeforeAPI(t *testing.T) {
	data, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}

	var compose composeFile
	if err := yaml.Unmarshal(data, &compose); err != nil {
		t.Fatalf("parse compose.yaml: %v", err)
	}

	apiService := compose.Services["api"]
	for _, dependency := range []string{"opengauss", "redis"} {
		service, ok := compose.Services[dependency]
		if !ok {
			t.Errorf("compose service %q is missing", dependency)
			continue
		}
		if len(service.Healthcheck) == 0 {
			t.Errorf("compose service %q has no healthcheck", dependency)
		}
		if got := apiService.DependsOn[dependency].Condition; got != "service_healthy" {
			t.Errorf("api dependency %q condition = %q, want service_healthy", dependency, got)
		}
	}

	if got := apiService.Environment["REDIS_ADDR"]; got != "redis:6379" {
		t.Errorf("REDIS_ADDR = %q, want redis:6379", got)
	}
	if got := apiService.Environment["DB_USER"]; got != "gaussdb" {
		t.Errorf("DB_USER = %q, want non-initial openGauss user gaussdb", got)
	}
	if got := apiService.Environment["DB_NAME"]; got != "guardianband" {
		t.Errorf("DB_NAME = %q, want guardianband", got)
	}
	if _, ok := compose.Volumes["redis-data"]; !ok {
		t.Error("redis-data volume is missing")
	}

	openGauss := compose.Services["opengauss"]
	if got := openGauss.Environment["GS_DB"]; got != "guardianband" {
		t.Errorf("openGauss GS_DB = %q, want guardianband", got)
	}
	if got := openGauss.Image; got != "opengauss/opengauss-server:latest" {
		t.Errorf("openGauss image = %q, want maintained multi-architecture server image", got)
	}
	if !contains(openGauss.Volumes, "opengauss-data:/var/lib/opengauss") {
		t.Errorf("openGauss volumes = %v, want persistent /var/lib/opengauss mount", openGauss.Volumes)
	}

	redisConfig, err := os.ReadFile("redis/redis.conf")
	if err != nil {
		t.Fatalf("read Redis config: %v", err)
	}
	for _, setting := range []string{
		"appendonly yes",
		"appendfsync everysec",
		"maxmemory-policy noeviction",
	} {
		if !strings.Contains(string(redisConfig), setting) {
			t.Errorf("Redis config does not contain %q", setting)
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
