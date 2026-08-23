package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

type recordingPolypharmacyClient struct {
	prediction PolypharmacyPrediction
	calls      []PolypharmacyPredictRequest
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func (c *recordingPolypharmacyClient) Predict(_ context.Context, drugA, drugB string, topK int) (PolypharmacyPrediction, error) {
	c.calls = append(c.calls, PolypharmacyPredictRequest{DrugA: drugA, DrugB: drugB, TopK: topK})
	return c.prediction, nil
}

func TestPolypharmacyHTTPClientPredict(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/predict" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req PolypharmacyPredictRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode AI request: %v", err)
		}
		if req.DrugA != "DB00316" || req.DrugB != "DB00945" || req.TopK != 5 {
			t.Errorf("unexpected AI request: %+v", req)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"pair_id":"DB00316__DB00945",
				"model":"polypharmacy",
				"mode":"fused",
				"confidence":"standard",
				"level_1_organs":[{"organ":"cardiovascular","probability":0.82}]
			}`)),
		}, nil
	})

	client, err := NewPolypharmacyHTTPClient("http://polypharmacy-ai:8000", time.Second)
	if err != nil {
		t.Fatalf("create AI client: %v", err)
	}
	client.client.Transport = transport
	prediction, err := client.Predict(context.Background(), "DB00316", "DB00945", 5)
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	if prediction.PairID != "DB00316__DB00945" || len(prediction.Level1Organs) != 1 {
		t.Errorf("unexpected prediction: %+v", prediction)
	}
}

func TestPostMedicationRiskAnalysisSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	client := &recordingPolypharmacyClient{prediction: PolypharmacyPrediction{
		PairID: "DB00316__DB00945", Model: "polypharmacy", Mode: "fused", Confidence: "standard",
		Level1Organs:      []PolypharmacyOrganRisk{{Organ: "cardiovascular", Probability: 0.82}},
		Level2SideEffects: []PolypharmacySideEffectRisk{{Label: "nausea", CUI: "C0027497", Probability: 0.41}},
		InferenceMS:       1.2,
	}}
	a := &API{db: db, polypharmacyClient: client}
	userID := "607d83ca-be13-4258-88a4-c56adcec91d8"
	med1 := "711a1234-abcd-ef01-2345-6789abcdef01"
	med2 := "722a1234-abcd-ef01-2345-6789abcdef02"

	mock.ExpectQuery("SELECT m.id, m.name, i.ingredient_name, i.drugbank_id, i.ai_supported").
		WithArgs(userID, med1, med2).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "ingredient_name", "drugbank_id", "ai_supported"}).
			AddRow(med1, "Parol", "acetaminophen", "DB00316", true).
			AddRow(med2, "Aspirin", "acetylsalicylic acid", "DB00945", true))

	body, _ := json.Marshal(MedicationRiskAnalysisRequest{MedicationIDs: []string{med1, med2}, TopK: 5})
	req := httptest.NewRequest(http.MethodPost, "/api/medication-risk-analyses", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()

	a.PostMedicationRiskAnalysisHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp MedicationRiskAnalysisResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Pairs) != 1 || len(resp.Pairs[0].OrganRisks) != 1 || resp.Pairs[0].OrganRisks[0].Organ != "cardiovascular" {
		t.Errorf("unexpected analysis: %+v", resp)
	}
	if len(client.calls) != 1 || client.calls[0].DrugA != "DB00316" || client.calls[0].DrugB != "DB00945" {
		t.Errorf("unexpected AI calls: %+v", client.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestPostMedicationRiskAnalysisRejectsUnownedOrMissingMedication(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := &recordingPolypharmacyClient{}
	a := &API{db: db, polypharmacyClient: client}
	userID := "607d83ca-be13-4258-88a4-c56adcec91d8"
	med1 := "711a1234-abcd-ef01-2345-6789abcdef01"
	med2 := "722a1234-abcd-ef01-2345-6789abcdef02"

	mock.ExpectQuery("SELECT m.id, m.name, i.ingredient_name, i.drugbank_id, i.ai_supported").
		WithArgs(userID, med1, med2).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "ingredient_name", "drugbank_id", "ai_supported"}).
			AddRow(med1, "Parol", "acetaminophen", "DB00316", true))

	body, _ := json.Marshal(MedicationRiskAnalysisRequest{MedicationIDs: []string{med1, med2}})
	req := httptest.NewRequest(http.MethodPost, "/api/medication-risk-analyses", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()
	a.PostMedicationRiskAnalysisHandler(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(client.calls) != 0 {
		t.Errorf("AI was called before ownership was established: %+v", client.calls)
	}
}

func TestPostMedicationRiskAnalysisRejectsUnsupportedIngredient(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := &recordingPolypharmacyClient{}
	a := &API{db: db, polypharmacyClient: client}
	userID := "607d83ca-be13-4258-88a4-c56adcec91d8"
	med1 := "711a1234-abcd-ef01-2345-6789abcdef01"
	med2 := "722a1234-abcd-ef01-2345-6789abcdef02"

	mock.ExpectQuery("SELECT m.id, m.name, i.ingredient_name, i.drugbank_id, i.ai_supported").
		WithArgs(userID, med1, med2).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "ingredient_name", "drugbank_id", "ai_supported"}).
			AddRow(med1, "Parol", "acetaminophen", "DB00316", true).
			AddRow(med2, "Lipitor", "atorvastatin", "DB01076", false))

	body, _ := json.Marshal(MedicationRiskAnalysisRequest{MedicationIDs: []string{med1, med2}})
	req := httptest.NewRequest(http.MethodPost, "/api/medication-risk-analyses", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()
	a.PostMedicationRiskAnalysisHandler(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected status 422, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp UnsupportedMedicationsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Code != "unsupported_medication" || len(resp.Unsupported) != 1 || resp.Unsupported[0].DrugBankID != "DB01076" {
		t.Errorf("unexpected unsupported response: %+v", resp)
	}
	if len(client.calls) != 0 {
		t.Errorf("AI was called for an unsupported ingredient: %+v", client.calls)
	}
}

func TestPostMedicationRiskAnalysisRejectsPairExplosionBeforeCallingAI(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := &recordingPolypharmacyClient{}
	a := &API{db: db, polypharmacyClient: client}
	userID := "607d83ca-be13-4258-88a4-c56adcec91d8"
	medicationIDs := make([]string, 10)
	rows := sqlmock.NewRows([]string{"id", "name", "ingredient_name", "drugbank_id", "ai_supported"})
	for i := range medicationIDs {
		medicationIDs[i] = fmt.Sprintf("%08d-abcd-4f01-8345-6789abcdef01", i+1)
		rows.AddRow(medicationIDs[i], fmt.Sprintf("Medication %d", i+1), "ingredient a", fmt.Sprintf("DB%05d", i*2+1), true)
		rows.AddRow(medicationIDs[i], fmt.Sprintf("Medication %d", i+1), "ingredient b", fmt.Sprintf("DB%05d", i*2+2), true)
	}
	mock.ExpectQuery("SELECT m.id, m.name, i.ingredient_name, i.drugbank_id, i.ai_supported").WillReturnRows(rows)

	body, _ := json.Marshal(MedicationRiskAnalysisRequest{MedicationIDs: medicationIDs})
	req := httptest.NewRequest(http.MethodPost, "/api/medication-risk-analyses", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey, userID))
	rec := httptest.NewRecorder()
	a.PostMedicationRiskAnalysisHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(client.calls) != 0 {
		t.Fatalf("AI received %d calls before pair-count rejection", len(client.calls))
	}
}
