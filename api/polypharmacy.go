package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	defaultPolypharmacyTopK = 5
	maxPolypharmacyTopK     = 20
	maxRiskMedications      = 10
	maxRiskPairs            = 100
	maxAIResponseBytes      = 1 << 20
)

// PolypharmacyClient is the internal boundary to the AI inference service.
type PolypharmacyClient interface {
	Predict(ctx context.Context, drugA, drugB string, topK int) (PolypharmacyPrediction, error)
}

// PolypharmacyPredictRequest is the AI service request contract.
type PolypharmacyPredictRequest struct {
	DrugA string `json:"drug_a"`
	DrugB string `json:"drug_b"`
	TopK  int    `json:"top_k"`
}

type PolypharmacyOrganRisk struct {
	Organ       string  `json:"organ"`
	Probability float64 `json:"probability"`
}

type PolypharmacySideEffectRisk struct {
	Label       string  `json:"label"`
	CUI         string  `json:"cui"`
	Probability float64 `json:"probability"`
}

// PolypharmacyPrediction mirrors the stable response returned by the AI service.
type PolypharmacyPrediction struct {
	PairID            string                       `json:"pair_id"`
	DrugA             string                       `json:"drug_a"`
	DrugB             string                       `json:"drug_b"`
	Model             string                       `json:"model"`
	Mode              string                       `json:"mode"`
	Confidence        string                       `json:"confidence"`
	Warnings          []string                     `json:"warnings"`
	Level1Organs      []PolypharmacyOrganRisk      `json:"level_1_organs"`
	Level2SideEffects []PolypharmacySideEffectRisk `json:"level_2_side_effects"`
	InferenceMS       float64                      `json:"inference_ms"`
}

type polypharmacyHTTPClient struct {
	baseURL string
	client  *http.Client
}

type polypharmacyServiceError struct {
	StatusCode int
}

func (e *polypharmacyServiceError) Error() string {
	return fmt.Sprintf("polypharmacy service returned status %d", e.StatusCode)
}

// NewPolypharmacyHTTPClient creates a bounded client for an operator-controlled service URL.
func NewPolypharmacyHTTPClient(baseURL string, timeout time.Duration) (*polypharmacyHTTPClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("AI_BASE_URL must be an http(s) service URL without credentials, query, or fragment")
	}
	if timeout <= 0 {
		return nil, errors.New("AI_REQUEST_TIMEOUT must be positive")
	}
	return &polypharmacyHTTPClient{
		baseURL: strings.TrimRight(parsed.String(), "/"),
		client:  &http.Client{Timeout: timeout},
	}, nil
}

func (c *polypharmacyHTTPClient) Predict(ctx context.Context, drugA, drugB string, topK int) (PolypharmacyPrediction, error) {
	var prediction PolypharmacyPrediction
	body, err := json.Marshal(PolypharmacyPredictRequest{DrugA: drugA, DrugB: drugB, TopK: topK})
	if err != nil {
		return prediction, fmt.Errorf("encode prediction request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/predict", bytes.NewReader(body))
	if err != nil {
		return prediction, fmt.Errorf("create prediction request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return prediction, fmt.Errorf("call polypharmacy service: %w", err)
	}
	defer resp.Body.Close()

	limited := &io.LimitedReader{R: resp.Body, N: maxAIResponseBytes + 1}
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		return prediction, fmt.Errorf("read prediction response: %w", err)
	}
	if len(responseBody) > maxAIResponseBytes {
		return prediction, errors.New("polypharmacy service response exceeded size limit")
	}
	if resp.StatusCode != http.StatusOK {
		return prediction, &polypharmacyServiceError{StatusCode: resp.StatusCode}
	}
	if err := json.Unmarshal(responseBody, &prediction); err != nil {
		return prediction, fmt.Errorf("decode prediction response: %w", err)
	}
	return prediction, nil
}

type MedicationRiskAnalysisRequest struct {
	MedicationIDs []string `json:"medicationIds"`
	TopK          int      `json:"topK"`
}

type MedicationRiskParticipant struct {
	MedicationID string `json:"medicationId"`
	Name         string `json:"name"`
	Ingredient   string `json:"ingredient"`
	DrugBankID   string `json:"drugBankId"`
}

type MedicationRiskPair struct {
	MedicationA     MedicationRiskParticipant    `json:"medicationA"`
	MedicationB     MedicationRiskParticipant    `json:"medicationB"`
	Model           string                       `json:"model"`
	Mode            string                       `json:"mode"`
	Confidence      string                       `json:"confidence"`
	Warnings        []string                     `json:"warnings"`
	OrganRisks      []PolypharmacyOrganRisk      `json:"organRisks"`
	SideEffectRisks []PolypharmacySideEffectRisk `json:"sideEffectRisks"`
	InferenceMS     float64                      `json:"inferenceMs"`
}

type MedicationRiskAnalysisResponse struct {
	Pairs       []MedicationRiskPair `json:"pairs"`
	GeneratedAt time.Time            `json:"generatedAt"`
	Disclaimer  string               `json:"disclaimer"`
}

type UnsupportedMedication struct {
	MedicationID string `json:"medicationId"`
	Name         string `json:"name"`
	Ingredient   string `json:"ingredient,omitempty"`
	DrugBankID   string `json:"drugBankId,omitempty"`
}

type UnsupportedMedicationsResponse struct {
	Code        string                  `json:"code"`
	Unsupported []UnsupportedMedication `json:"unsupported"`
}

type medicationIngredientRecord struct {
	MedicationRiskParticipant
	Supported bool
}

type medicationRiskPairCandidate struct {
	medicationA MedicationRiskParticipant
	medicationB MedicationRiskParticipant
}

// PostMedicationRiskAnalysisHandler evaluates supported ingredients in owned, active medications.
func (a *API) PostMedicationRiskAnalysisHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserContextKey).(string)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req MedicationRiskAnalysisRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.MedicationIDs) < 2 || len(req.MedicationIDs) > maxRiskMedications {
		http.Error(w, "medicationIds must contain between 2 and 10 items", http.StatusBadRequest)
		return
	}
	if req.TopK == 0 {
		req.TopK = defaultPolypharmacyTopK
	}
	if req.TopK < 1 || req.TopK > maxPolypharmacyTopK {
		http.Error(w, "topK must be between 1 and 20", http.StatusBadRequest)
		return
	}

	seen := make(map[string]struct{}, len(req.MedicationIDs))
	args := make([]any, 0, len(req.MedicationIDs)+1)
	args = append(args, userID)
	placeholders := make([]string, len(req.MedicationIDs))
	for i, medicationID := range req.MedicationIDs {
		if _, err := uuid.Parse(medicationID); err != nil {
			http.Error(w, "medicationIds must contain valid UUIDs", http.StatusBadRequest)
			return
		}
		if _, duplicate := seen[medicationID]; duplicate {
			http.Error(w, "medicationIds must not contain duplicates", http.StatusBadRequest)
			return
		}
		seen[medicationID] = struct{}{}
		args = append(args, medicationID)
		placeholders[i] = fmt.Sprintf("$%d", i+2)
	}

	query := `
		SELECT m.id, m.name, i.ingredient_name, i.drugbank_id, i.ai_supported
		FROM medications m
		LEFT JOIN medication_catalog_ingredients i ON i.catalog_item_id = m.catalog_item_id
		WHERE m.patient_id = $1 AND m.active = true AND m.id IN (` + strings.Join(placeholders, ", ") + `)
		ORDER BY m.id, i.drugbank_id`
	rows, err := a.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "failed to load medications", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	records := make([]medicationIngredientRecord, 0)
	found := make(map[string]struct{}, len(req.MedicationIDs))
	unsupported := make([]UnsupportedMedication, 0)
	for rows.Next() {
		var medicationID, name string
		var ingredient, drugBankID sql.NullString
		var supported sql.NullBool
		if err := rows.Scan(&medicationID, &name, &ingredient, &drugBankID, &supported); err != nil {
			http.Error(w, "failed to read medications", http.StatusInternalServerError)
			return
		}
		found[medicationID] = struct{}{}
		record := medicationIngredientRecord{MedicationRiskParticipant: MedicationRiskParticipant{
			MedicationID: medicationID, Name: name, Ingredient: ingredient.String, DrugBankID: drugBankID.String,
		}, Supported: ingredient.Valid && drugBankID.Valid && supported.Valid && supported.Bool}
		records = append(records, record)
		if !record.Supported {
			unsupported = append(unsupported, UnsupportedMedication{
				MedicationID: medicationID, Name: name, Ingredient: ingredient.String, DrugBankID: drugBankID.String,
			})
		}
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read medications", http.StatusInternalServerError)
		return
	}
	if len(found) != len(req.MedicationIDs) {
		http.Error(w, "one or more active medications were not found", http.StatusNotFound)
		return
	}
	if len(unsupported) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, UnsupportedMedicationsResponse{Code: "unsupported_medication", Unsupported: unsupported})
		return
	}
	if a.polypharmacyClient == nil {
		http.Error(w, "medication risk analysis is unavailable", http.StatusServiceUnavailable)
		return
	}

	candidates := make([]medicationRiskPairCandidate, 0)
	pairKeys := make(map[string]struct{})
	for i := 0; i < len(records); i++ {
		for j := i + 1; j < len(records); j++ {
			if records[i].MedicationID == records[j].MedicationID || records[i].DrugBankID == records[j].DrugBankID {
				continue
			}
			key := records[i].MedicationID + ":" + records[i].DrugBankID + "|" + records[j].MedicationID + ":" + records[j].DrugBankID
			if _, exists := pairKeys[key]; exists {
				continue
			}
			if len(candidates) >= maxRiskPairs {
				http.Error(w, "medication selection produces too many ingredient pairs", http.StatusBadRequest)
				return
			}
			pairKeys[key] = struct{}{}
			candidates = append(candidates, medicationRiskPairCandidate{
				medicationA: records[i].MedicationRiskParticipant,
				medicationB: records[j].MedicationRiskParticipant,
			})
		}
	}
	if len(candidates) == 0 {
		http.Error(w, "medications do not produce a distinct ingredient pair", http.StatusUnprocessableEntity)
		return
	}

	pairs := make([]MedicationRiskPair, 0, len(candidates))
	analysisContext, cancelAnalysis := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancelAnalysis()
	for _, candidate := range candidates {
		prediction, err := a.polypharmacyClient.Predict(analysisContext, candidate.medicationA.DrugBankID, candidate.medicationB.DrugBankID, req.TopK)
		if err != nil {
			var serviceErr *polypharmacyServiceError
			if errors.As(err, &serviceErr) && serviceErr.StatusCode == http.StatusUnprocessableEntity {
				writeJSON(w, http.StatusUnprocessableEntity, UnsupportedMedicationsResponse{Code: "unsupported_medication", Unsupported: unsupported})
				return
			}
			http.Error(w, "medication risk analysis is unavailable", http.StatusServiceUnavailable)
			return
		}
		pairs = append(pairs, MedicationRiskPair{
			MedicationA: candidate.medicationA, MedicationB: candidate.medicationB,
			Model: prediction.Model, Mode: prediction.Mode, Confidence: prediction.Confidence,
			Warnings: prediction.Warnings, OrganRisks: prediction.Level1Organs,
			SideEffectRisks: prediction.Level2SideEffects, InferenceMS: prediction.InferenceMS,
		})
	}

	writeJSON(w, http.StatusOK, MedicationRiskAnalysisResponse{
		Pairs: pairs, GeneratedAt: time.Now().UTC(),
		Disclaimer: "This prediction supports clinical review and is not a diagnosis or prescribing instruction.",
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
