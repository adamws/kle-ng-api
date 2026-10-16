package kicad

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// The kle-ng default preset: SW pad 2 to D pad 2 (A) on B.Cu for
// an MX switch with a SOD-123F diode at 5.08 4 90 BACK.
const defaultPresetJSON = `{"version": 1, "items": [
	{"type": "track", "start": {"x_nm": 2540000, "y_nm": -5080000}, "end": {"x_nm": 2540000, "y_nm": 60000}, "width_nm": 250000, "layer": "B.Cu"},
	{"type": "track", "start": {"x_nm": 2540000, "y_nm": 60000}, "end": {"x_nm": 5080000, "y_nm": 2600000}, "width_nm": 250000, "layer": "B.Cu"}
]}`

// settingsWithPreset decodes presetJSON the way the worker decodes a task
// request (numbers become float64) and returns it as settings.trackPreset.
func settingsWithPreset(t *testing.T, presetJSON string) map[string]interface{} {
	t.Helper()
	var settings map[string]interface{}
	if err := json.Unmarshal([]byte(`{"trackPreset": `+presetJSON+`}`), &settings); err != nil {
		t.Fatalf("bad test JSON: %v", err)
	}
	return settings
}

func argValue(t *testing.T, args []string, flag string) string {
	t.Helper()
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("%s not found in %v", flag, args)
	return ""
}

func TestBuildKBPlacerArgsDiode(t *testing.T) {
	opts := KBPlacerOptions{
		SwitchRotation: 0,
		SwitchSide:     "FRONT",
		DiodeRotation:  90,
		DiodeSide:      "BACK",
		DiodePositionX: 5.08,
		DiodePositionY: 4,
	}

	got := argValue(t, buildKBPlacerArgs(opts), "--diode")
	if want := "D{} CUSTOM 5.080000 4.000000 90 BACK"; got != want {
		t.Errorf("without preset: --diode %q, want %q", got, want)
	}

	opts.DiodePresetPath = "/tmp/task/logs/switch-preset.json"
	args := buildKBPlacerArgs(opts)
	got = argValue(t, args, "--diode")
	if want := "D{} PRESET /tmp/task/logs/switch-preset.json"; got != want {
		t.Errorf("with preset: --diode %q, want %q", got, want)
	}
	n := 0
	for _, a := range args {
		if a == "--diode" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("expected exactly one --diode, got %d", n)
	}
}

func TestParseTrackPresetAbsent(t *testing.T) {
	for name, settings := range map[string]map[string]interface{}{
		"missing": {},
		"null":    {"trackPreset": nil},
	} {
		preset, err := parseTrackPreset(settings)
		if err != nil || preset != nil {
			t.Errorf("%s: got (%v, %v), want (nil, nil)", name, preset, err)
		}
	}
}

func TestParseTrackPresetPassesFormatThrough(t *testing.T) {
	// The format is validated by kbplacer, anything shaped like an object
	// passes, including content kbplacer will reject.
	for _, presetJSON := range []string{
		defaultPresetJSON,
		`{"version": 2, "items": [{"type": "arc", "mid": {"x_nm": 0.5}}], "basis": {}}`,
		`{"version": 1, "items": []}`,
		`{}`,
	} {
		settings := settingsWithPreset(t, presetJSON)
		preset, err := parseTrackPreset(settings)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", presetJSON, err)
			continue
		}
		if !reflect.DeepEqual(map[string]interface{}(preset), settings["trackPreset"]) {
			t.Errorf("%s: preset changed: %v", presetJSON, preset)
		}
	}
}

func TestParseTrackPresetInvalid(t *testing.T) {
	item := `{"type": "via", "position": {"x_nm": 0, "y_nm": 0}, "diameter_nm": 600000, "drill_diameter_nm": 300000}`
	tooManyItems := strings.TrimSuffix(strings.Repeat(item+",", maxTrackPresetItems+1), ",")

	cases := []struct {
		name    string
		preset  string
		wantMsg string
	}{
		{"not an object", `[1]`, "must be an object"},
		{"string", `"preset"`, "must be an object"},
		{"diodes set", `{"version": 1, "diodes": [], "items": []}`, "diodes must not be set"},
		{"too many items", `{"version": 1, "items": [` + tooManyItems + `]}`, "too many items (501)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseTrackPreset(settingsWithPreset(t, tc.preset))
			if err == nil {
				t.Fatalf("expected error")
			}
			if !errors.Is(err, ErrInvalidTrackPreset) {
				t.Errorf("error %v does not wrap ErrInvalidTrackPreset", err)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not contain %q", err, tc.wantMsg)
			}
		})
	}
}

func TestWriteSwitchPresetFile(t *testing.T) {
	settings := settingsWithPreset(t, defaultPresetJSON)
	preset, err := parseTrackPreset(settings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	path := filepath.Join(t.TempDir(), "switch-preset.json")
	if err := WriteSwitchPresetFile(path, preset, 5.08, 4, 90, "BACK"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// the preset is written unchanged, with the diodes added
	var got, want interface{}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("written file is not JSON: %v\n%s", err, data)
	}
	wantJSON := `{
		"version": 1,
		"diodes": [{"position": {"x_nm": 5080000, "y_nm": 4000000}, "orientation_deg": 90, "layer": "B.Cu"}],
		"items": [
			{"type": "track", "start": {"x_nm": 2540000, "y_nm": -5080000}, "end": {"x_nm": 2540000, "y_nm": 60000}, "width_nm": 250000, "layer": "B.Cu"},
			{"type": "track", "start": {"x_nm": 2540000, "y_nm": 60000}, "end": {"x_nm": 5080000, "y_nm": 2600000}, "width_nm": 250000, "layer": "B.Cu"}
		]
	}`
	if err := json.Unmarshal([]byte(wantJSON), &want); err != nil {
		t.Fatalf("bad test JSON: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("written preset mismatch\n got: %s", data)
	}
	// integers stay integers, kbplacer rejects non-integer nanometres
	if regexp.MustCompile(`\d(\.\d|e[+-])`).Match(data) {
		t.Errorf("written preset has non-integer numbers:\n%s", data)
	}
	if _, ok := settings["trackPreset"].(map[string]interface{})["diodes"]; ok {
		t.Errorf("request settings were modified")
	}
}

func TestBuildSwitchPresetFileDiodeMapping(t *testing.T) {
	cases := []struct {
		x, y     float64
		rotation int
		side     string
		want     string
	}{
		{5.08, 4, 90, "BACK", `[{"layer":"B.Cu","orientation_deg":90,"position":{"x_nm":5080000,"y_nm":4000000}}]`},
		{-5, -4.5, 270, "FRONT", `[{"layer":"F.Cu","orientation_deg":270,"position":{"x_nm":-5000000,"y_nm":-4500000}}]`},
		// float mm values are rounded, not truncated, to the nearest nm
		{0.1 + 0.2, 1.0000004, 0, "FRONT", `[{"layer":"F.Cu","orientation_deg":0,"position":{"x_nm":300000,"y_nm":1000000}}]`},
	}
	for _, tc := range cases {
		file := buildSwitchPresetFile(TrackPreset{}, tc.x, tc.y, tc.rotation, tc.side)
		got, err := json.Marshal(file["diodes"])
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(got) != tc.want {
			t.Errorf("diode %v %v %d %s: got %s, want %s", tc.x, tc.y, tc.rotation, tc.side, got, tc.want)
		}
	}
}

// The stabilizer library must be bundled and listed in fp-lib-table next to
// the switch library, a repeated library only once.
func TestBundleFootprintLibraries(t *testing.T) {
	srcRoot := t.TempDir()
	projectPath := t.TempDir()
	for _, lib := range []string{"Switch_Keyboard_Cherry_MX", "Mounting_Keyboard_Stabilizer"} {
		dir := filepath.Join(srcRoot, lib+".pretty")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, lib+"_fp.kicad_mod"), []byte("(footprint)"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	libs := []string{"Switch_Keyboard_Cherry_MX", "Mounting_Keyboard_Stabilizer", "Switch_Keyboard_Cherry_MX"}
	if err := bundleFootprintLibraries(srcRoot, projectPath, libs); err != nil {
		t.Fatalf("bundleFootprintLibraries: %v", err)
	}

	for _, lib := range libs[:2] {
		fp := filepath.Join(projectPath, "footprints", lib+".pretty", lib+"_fp.kicad_mod")
		if _, err := os.Stat(fp); err != nil {
			t.Errorf("%s not bundled: %v", fp, err)
		}
	}

	table, err := os.ReadFile(filepath.Join(projectPath, "fp-lib-table"))
	if err != nil {
		t.Fatal(err)
	}
	want := `(fp_lib_table
   (version 7)
   (lib (name "Switch_Keyboard_Cherry_MX")(type "KiCad")(uri "${KIPRJMOD}/footprints/Switch_Keyboard_Cherry_MX.pretty")(options "")(descr ""))
   (lib (name "Mounting_Keyboard_Stabilizer")(type "KiCad")(uri "${KIPRJMOD}/footprints/Mounting_Keyboard_Stabilizer.pretty")(options "")(descr ""))
)
`
	if string(table) != want {
		t.Errorf("fp-lib-table =\n%s\nwant\n%s", table, want)
	}
}
