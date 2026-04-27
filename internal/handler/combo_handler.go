package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"genfity-ai-gateway-service/internal/service"
)

type ComboHandler struct {
	combos *service.ComboService
}

func NewComboHandler(combos *service.ComboService) *ComboHandler {
	return &ComboHandler{combos: combos}
}

func (h *ComboHandler) ListCombos(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{"combos": h.combos.ListCombos(r.Context())})
}

func (h *ComboHandler) CreateCombo(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	combo, err := h.combos.UpsertCombo(r.Context(), payload)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusCreated, combo)
}

func (h *ComboHandler) UpdateCombo(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("comboID")
	id, err := uuid.Parse(idStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_combo_id")
		return
	}
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	payload["id"] = id.String()
	combo, err := h.combos.UpsertCombo(r.Context(), payload)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, combo)
}

func (h *ComboHandler) DeleteCombo(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("comboID")
	id, err := uuid.Parse(idStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_combo_id")
		return
	}
	if err := h.combos.DeleteCombo(r.Context(), id); err != nil {
		respondError(w, http.StatusNotFound, "combo_not_found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true})
}
