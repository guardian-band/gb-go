package main

import (
	"os"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestOpenAPIDocumentsMedicationRiskAnalysis(t *testing.T) {
	data, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}
	paths, ok := document["paths"].(map[string]any)
	if !ok {
		t.Fatal("OpenAPI paths are missing")
	}
	if _, ok := paths["/api/medication-risk-analyses"]; !ok {
		t.Error("medication risk analysis path is undocumented")
	}
	components, ok := document["components"].(map[string]any)
	if !ok {
		t.Fatal("OpenAPI components are missing")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatal("OpenAPI schemas are missing")
	}
	for _, name := range []string{"MedicationIngredient", "MedicationRiskAnalysisRequest", "MedicationRiskAnalysisResponse"} {
		if _, ok := schemas[name]; !ok {
			t.Errorf("OpenAPI schema %q is missing", name)
		}
	}
}
