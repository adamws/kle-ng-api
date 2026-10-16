package kicad

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
)

// Switch-diode track preset from settings.trackPreset, written for kbplacer's
// `--diode "D{} PRESET <file>.json"`. The format belongs to kbplacer
// (kicad-kbplacer's docs/switch-preset.schema.json) and kbplacer validates it,
// so the preset is passed through untouched. This service only adds the diode
// placement from the diode settings and enforces its own size limit.

// maxTrackPresetItems limits the work a single request can ask for.
const maxTrackPresetItems = 500

// TrackPreset is settings.trackPreset as decoded from the task request.
type TrackPreset map[string]interface{}

// parseTrackPreset returns settings.trackPreset, or nil when the field is
// absent or null.
func parseTrackPreset(settings map[string]interface{}) (TrackPreset, error) {
	raw, ok := settings["trackPreset"]
	if !ok || raw == nil {
		return nil, nil
	}
	preset, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: must be an object", ErrInvalidTrackPreset)
	}
	// Diode placement comes from the diode settings only, so there is a single
	// source of truth for it.
	if _, ok := preset["diodes"]; ok {
		return nil, fmt.Errorf("%w: diodes must not be set, diode placement comes from the diode settings", ErrInvalidTrackPreset)
	}
	if items, ok := preset["items"].([]interface{}); ok && len(items) > maxTrackPresetItems {
		return nil, fmt.Errorf("%w: too many items (%d), at most %d allowed", ErrInvalidTrackPreset, len(items), maxTrackPresetItems)
	}
	return preset, nil
}

func sideToLayer(side string) string {
	if side == "BACK" {
		return "B.Cu"
	}
	return "F.Cu"
}

// buildSwitchPresetFile returns a copy of the preset with the diode placement
// added, in the preset's integer nanometres and KiCad layer names.
func buildSwitchPresetFile(preset TrackPreset, diodeX, diodeY float64, diodeRotation int, diodeSide string) map[string]interface{} {
	file := make(map[string]interface{}, len(preset)+1)
	for k, v := range preset {
		file[k] = v
	}
	file["diodes"] = []interface{}{
		map[string]interface{}{
			"position": map[string]interface{}{
				"x_nm": int64(math.Round(diodeX * 1e6)),
				"y_nm": int64(math.Round(diodeY * 1e6)),
			},
			"orientation_deg": diodeRotation,
			"layer":           sideToLayer(diodeSide),
		},
	}
	return file
}

// WriteSwitchPresetFile writes the kbplacer preset file to path.
func WriteSwitchPresetFile(path string, preset TrackPreset, diodeX, diodeY float64, diodeRotation int, diodeSide string) error {
	data, err := json.MarshalIndent(buildSwitchPresetFile(preset, diodeX, diodeY, diodeRotation, diodeSide), "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal switch preset: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write switch preset: %w", err)
	}
	return nil
}
