package kicad

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Constants for SVG export templates and library paths
const (
	SVGTemplateFront    = "F.Cu,F.SilkS,Edge.Cuts"
	SVGTemplateBack     = "B.Cu,B.SilkS,Edge.Cuts"
	SwitchesLibraryPath = "/footprints/com_github_perigoso_keyswitch-kicad-library/"
	DiodeLibraryPath    = "/usr/share/kicad/footprints/"
)

// KBPlacerOptions holds the configuration used to build a kbplacer invocation.
type KBPlacerOptions struct {
	PCBPath                 string
	LayoutPath              string
	RouteSwitchesWithDiodes bool
	RouteRowsAndColumns     bool
	SwitchFootprint         string
	DiodeFootprint          string
	StabilizerFootprint     string
	SwitchRotation          int
	SwitchSide              string
	DiodeRotation           int
	DiodeSide               string
	DiodePositionX          float64
	DiodePositionY          float64

	// LED chain options. LedFootprint/LedCapacitorFootprint are shared between
	// the PCB (CreateLedPcbElements) and schematic (CreateLedSchFile) paths.
	CreateLedPcbElements  bool
	CreateLedSchFile      bool
	LedFootprint          string
	LedCapacitorFootprint string
	SkipLedDecoupling     bool

	LedRotation  int
	LedSide      string
	LedPositionX float64
	LedPositionY float64

	LedCapacitorRotation  int
	LedCapacitorSide      string
	LedCapacitorPositionX float64
	LedCapacitorPositionY float64
}

func buildKBPlacerArgs(opts KBPlacerOptions) []string {
	args := []string{
		"-m", "kbplacer",
		"--pcb-file", opts.PCBPath,
		"--create-sch-file",
		"--create-pcb-file",
		"--switch-footprint", opts.SwitchFootprint,
		"--diode-footprint", opts.DiodeFootprint,
		"--encoder-footprint", "/usr/share/kicad/footprints/Rotary_Encoder.pretty:RotaryEncoder_Alps_EC11E-Switch_Vertical_H20mm",
		"--encoder-adjustment", "-7.5 -2.5",
		"--layout", opts.LayoutPath,
		"--layout-offset", "0 0",
		"--log-level", "INFO",
		"--max-keys", "150",
	}

	// Add conditional flags
	if opts.RouteSwitchesWithDiodes {
		args = append(args, "--route-switches-with-diodes")
	}
	if opts.RouteRowsAndColumns {
		args = append(args, "--route-rows-and-columns")
	}

	// Add switch configuration argument
	// Format: --switch "SW{} <rotation> <side>"
	switchArg := fmt.Sprintf("SW{} %d %s", opts.SwitchRotation, opts.SwitchSide)
	args = append(args, "--switch", switchArg)

	// Add diode configuration argument
	// Format: --diode "D{} CUSTOM <x> <y> <rotation> <side>"
	diodeArg := fmt.Sprintf("D{} CUSTOM %f %f %d %s", opts.DiodePositionX, opts.DiodePositionY, opts.DiodeRotation, opts.DiodeSide)
	args = append(args, "--diode", diodeArg)

	// --additional-elements is a ';' separated list controlling placement of
	// non-switch footprints (stabilizers, LEDs, decoupling capacitors).
	var additionalElements []string

	// Add stabilizer configuration
	if opts.StabilizerFootprint == "" {
		args = append(args, "--no-stabilizers")
	} else {
		args = append(args, "--stabilizer-footprint", opts.StabilizerFootprint)
		additionalElements = append(additionalElements, fmt.Sprintf("ST{} CUSTOM 0 0 0 %s", opts.SwitchSide))
	}

	// LED chain: the footprint settings feed both the PCB and schematic builders.
	if opts.CreateLedPcbElements || opts.CreateLedSchFile {
		if opts.LedFootprint != "" {
			args = append(args, "--led-footprint", opts.LedFootprint)
		}
		if opts.SkipLedDecoupling {
			args = append(args, "--skip-led-decoupling")
		} else if opts.LedCapacitorFootprint != "" {
			args = append(args, "--led-capacitor-footprint", opts.LedCapacitorFootprint)
		}
	}
	if opts.CreateLedPcbElements {
		args = append(args, "--create-led-pcb-elements")
		// Position one LED (and, unless skipped, one decoupling capacitor) per
		// key, relative to the switch it belongs to.
		ledArg := fmt.Sprintf("LED{} CUSTOM %f %f %d %s", opts.LedPositionX, opts.LedPositionY, opts.LedRotation, opts.LedSide)
		additionalElements = append(additionalElements, ledArg)
		if !opts.SkipLedDecoupling {
			ledCapacitorArg := fmt.Sprintf("C{} CUSTOM %f %f %d %s", opts.LedCapacitorPositionX, opts.LedCapacitorPositionY, opts.LedCapacitorRotation, opts.LedCapacitorSide)
			additionalElements = append(additionalElements, ledCapacitorArg)
		}
	}
	if opts.CreateLedSchFile {
		args = append(args, "--create-led-sch-file")
		// The key-matrix and LED-chain sheets are bundled into one project. We
		// run on KiCad 9, whose "flat" multi-sheet bundling is unavailable
		// (KiCad 10.0+ only), so request the hierarchical strategy explicitly:
		// a root .kicad_sch referencing both child sheets. This keeps output
		// deterministic regardless of the KiCad version kbplacer auto-detects.
		args = append(args, "--bundle-strategy", "hierarchical")
	}

	if len(additionalElements) > 0 {
		args = append(args, "--additional-elements", strings.Join(additionalElements, ";"))
	}

	return args
}

// RunKBPlacer runs the kbplacer tool to generate KiCad PCB and schematic files
func RunKBPlacer(ctx context.Context, opts KBPlacerOptions, logPath string) error {
	args := buildKBPlacerArgs(opts)

	// Create command with context (allows cancellation if task times out)
	cmd := exec.CommandContext(ctx, "python3", args...)

	// Open log file for output
	log.Printf("kbplacer logpath: %s", logPath)
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("failed to create log file: %w", err)
	}
	defer logFile.Close()

	// Redirect stdout and stderr to log file
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	// Start the process
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start kbplacer: %w", err)
	}

	// Wait for process to complete
	cmdErr := cmd.Wait()

	// Check if we had an error
	if cmdErr != nil {
		// Read log file to get detailed error information
		logFile.Close() // Close to ensure all content is flushed
		logContent, readErr := os.ReadFile(logPath)
		if readErr != nil {
			// If we can't read the log, return the generic error
			return fmt.Errorf("kbplacer failed: %w (unable to read log: %v)", cmdErr, readErr)
		}

		// Include log contents in error message for user feedback
		logStr := string(logContent)
		if len(logStr) > 5000 {
			// Truncate very long logs, keep last 5000 chars (most recent output)
			logStr = "...[truncated]...\n" + logStr[len(logStr)-5000:]
		}

		return fmt.Errorf("kbplacer failed: %w\n\nBuild log:\n%s", cmdErr, logStr)
	}

	return nil
}

func GetKicad3rdPartyPath() (string, error) {
	dirname, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	src := filepath.Join(dirname, ".local/share/kicad/9.0/3rdparty")
	return src, nil
}

// BundleSwitchFootprints copies switch footprint library into the project
func BundleSwitchFootprints(projectPath string, libNickname string) error {
	kicad3rdParty, err := GetKicad3rdPartyPath()
	if err != nil {
		return fmt.Errorf("failed to get KiCad 3rd party path: %w", err)
	}

	src := filepath.Join(kicad3rdParty, SwitchesLibraryPath, libNickname+".pretty")
	dst := filepath.Join(projectPath, "footprints", libNickname+".pretty")

	// Create destination directory
	if err := os.MkdirAll(dst, 0755); err != nil {
		return fmt.Errorf("failed to create footprints directory: %w", err)
	}

	// Copy directory contents
	if err := copyDir(src, dst); err != nil {
		return fmt.Errorf("failed to copy footprint library: %w", err)
	}

	// Write fp-lib-table file
	fpLibTablePath := filepath.Join(projectPath, "fp-lib-table")
	fpLibTableContent := fmt.Sprintf(`(fp_lib_table
   (version 7)
   (lib (name "%s")(type "KiCad")(uri "${KIPRJMOD}/footprints/%s.pretty")(options "")(descr ""))
)
`, libNickname, libNickname)

	if err := os.WriteFile(fpLibTablePath, []byte(fpLibTableContent), 0644); err != nil {
		return fmt.Errorf("failed to write fp-lib-table: %w", err)
	}

	return nil
}

// RunKiCadSVG exports PCB to SVG format
func RunKiCadSVG(pcbFile string, layers string, outputFile string) error {
	args := []string{
		"pcb", "export", "svg",
		"--layers", layers,
		"--exclude-drawing-sheet",
		"--fit-page-to-board",
		"--mode-single",
		"-o", outputFile,
		pcbFile,
	}

	cmd := exec.Command("kicad-cli", args...)

	// Capture output for error reporting
	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)
		if outputStr != "" {
			return fmt.Errorf("kicad-cli svg export failed: %w\nOutput:\n%s", err, outputStr)
		}
		return fmt.Errorf("kicad-cli svg export failed: %w", err)
	}

	return nil
}

// GenerateRender generates front and back SVG renders of the PCB
func GenerateRender(pcbPath string, logPath string) error {
	logDir := filepath.Dir(logPath)

	// Generate front render
	frontSVG := filepath.Join(logDir, "front.svg")
	if err := RunKiCadSVG(pcbPath, SVGTemplateFront, frontSVG); err != nil {
		return fmt.Errorf("failed to generate front render: %w", err)
	}

	// Generate back render
	backSVG := filepath.Join(logDir, "back.svg")
	if err := RunKiCadSVG(pcbPath, SVGTemplateBack, backSVG); err != nil {
		return fmt.Errorf("failed to generate back render: %w", err)
	}

	return nil
}

// GenerateSchematicImage exports schematic to SVG
func GenerateSchematicImage(schematicPath string, logPath string) error {
	schematicDir := filepath.Dir(schematicPath)
	logDir := filepath.Dir(logPath)

	args := []string{
		"sch", "export", "svg",
		"--exclude-drawing-sheet",
		"--output", schematicDir,
		schematicPath,
	}

	cmd := exec.Command("kicad-cli", args...)

	// Capture output for error reporting
	output, cmdErr := cmd.CombinedOutput()

	// Check if output file was created
	name := strings.TrimSuffix(filepath.Base(schematicPath), filepath.Ext(schematicPath))
	expectedResult := filepath.Join(schematicDir, name+".svg")

	if _, err := os.Stat(expectedResult); os.IsNotExist(err) {
		outputStr := string(output)
		if outputStr != "" && cmdErr != nil {
			return fmt.Errorf("failed to generate schematic image: %w\nOutput:\n%s", cmdErr, outputStr)
		} else if cmdErr != nil {
			return fmt.Errorf("failed to generate schematic image: %w", cmdErr)
		}
		return fmt.Errorf("failed to generate schematic image: output file not created")
	}

	// Copy to logs directory
	targetPath := filepath.Join(logDir, "schematic.svg")
	if err := copyFile(expectedResult, targetPath); err != nil {
		return fmt.Errorf("failed to copy schematic image: %w", err)
	}

	return nil
}

// CreateWorkDir creates a temporary work directory with task ID prefix
func CreateWorkDir(taskID string) (string, error) {
	workDir, err := os.MkdirTemp("", taskID)
	log.Printf("Created workdir: %s", workDir)
	if err != nil {
		return "", fmt.Errorf("failed to create work directory: %w", err)
	}

	absPath, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	return absPath, nil
}

// GetProjectName returns a sanitized project name from layout name
func GetProjectName(layoutName string) string {
	if layoutName == "" {
		return "keyboard"
	}
	return SanitizeFilename(layoutName)
}

// CreateKicadWorkDir creates the KiCad project directory
func CreateKicadWorkDir(workDir string, projectName string) (string, error) {
	projectDirName := SanitizeFilepath(projectName)
	projectDir := filepath.Join(workDir, projectDirName)

	if err := os.Mkdir(projectDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create project directory: %w", err)
	}

	absPath, err := filepath.Abs(projectDir)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	return absPath, nil
}

// CreateLogDir creates the logs directory
func CreateLogDir(workDir string) (string, error) {
	logDir := filepath.Join(workDir, "logs")

	if err := os.Mkdir(logDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create log directory: %w", err)
	}

	absPath, err := filepath.Abs(logDir)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	return absPath, nil
}

// parsePlacement extracts a CUSTOM element placement (rotation, side and X/Y
// offset) from the settings map using the given camelCase field prefix, e.g.
// "led" reads ledRotation/ledSide/ledPositionX/ledPositionY. Numbers arrive as
// float64 from JSON; the rotation is truncated to an int. The returned error
// names the offending field so callers can surface a precise message.
func parsePlacement(settings map[string]interface{}, prefix string) (int, string, float64, float64, error) {
	rotationKey := prefix + "Rotation"
	sideKey := prefix + "Side"
	xKey := prefix + "PositionX"
	yKey := prefix + "PositionY"

	rotationRaw, ok := settings[rotationKey]
	if !ok {
		return 0, "", 0, 0, fmt.Errorf("%w: %s", ErrMissingLedPlacement, rotationKey)
	}
	rotation, ok := rotationRaw.(float64)
	if !ok {
		return 0, "", 0, 0, fmt.Errorf("%w: %s must be a number", ErrInvalidLedPlacement, rotationKey)
	}

	sideRaw, ok := settings[sideKey]
	if !ok {
		return 0, "", 0, 0, fmt.Errorf("%w: %s", ErrMissingLedPlacement, sideKey)
	}
	side, ok := sideRaw.(string)
	if !ok {
		return 0, "", 0, 0, fmt.Errorf("%w: %s must be a string", ErrInvalidLedPlacement, sideKey)
	}
	if side != "FRONT" && side != "BACK" {
		return 0, "", 0, 0, fmt.Errorf("%w: %s must be FRONT or BACK", ErrInvalidLedPlacement, sideKey)
	}

	xRaw, ok := settings[xKey]
	if !ok {
		return 0, "", 0, 0, fmt.Errorf("%w: %s", ErrMissingLedPlacement, xKey)
	}
	x, ok := xRaw.(float64)
	if !ok {
		return 0, "", 0, 0, fmt.Errorf("%w: %s must be a number", ErrInvalidLedPlacement, xKey)
	}
	yRaw, ok := settings[yKey]
	if !ok {
		return 0, "", 0, 0, fmt.Errorf("%w: %s", ErrMissingLedPlacement, yKey)
	}
	y, ok := yRaw.(float64)
	if !ok {
		return 0, "", 0, 0, fmt.Errorf("%w: %s must be a number", ErrInvalidLedPlacement, yKey)
	}

	return int(rotation), side, x, y, nil
}

// NewPCB is the main entry point for generating a KiCad PCB project
func NewPCB(ctx context.Context, taskID string, taskRequest map[string]interface{}) (string, error) {
	startTime := time.Now()

	// Extract layout and settings from request
	layout, ok := taskRequest["layout"].(map[string]interface{})
	if !ok {
		return "", ErrInvalidLayout
	}

	settings, ok := taskRequest["settings"].(map[string]interface{})
	if !ok {
		return "", ErrInvalidSettings
	}

	// Parse settings
	switchFootprintSetting, _ := settings["switchFootprint"].(string)
	diodeFootprintSetting, _ := settings["diodeFootprint"].(string)
	stabilizerFootprintSetting, _ := settings["stabilizerFootprint"].(string)
	routing, _ := settings["routing"].(string)

	// LED chain settings (optional). A single toggle drives the whole chain:
	// whenever the LED-chain schematic is requested, the matching PCB elements
	// are generated too, so the board and schematic can never diverge.
	createLedSchFile, _ := settings["createLedSchFile"].(bool)
	createLedPcbElements := createLedSchFile
	skipLedDecoupling, _ := settings["skipLedDecoupling"].(bool)
	ledFootprintSetting, _ := settings["ledFootprint"].(string)
	ledCapacitorFootprintSetting, _ := settings["ledCapacitorFootprint"].(string)

	// LED and decoupling capacitor placement (CUSTOM positioning relative to the
	// key). Only parsed/validated when the LED chain is enabled - see below.
	var (
		ledRotationInt          int
		ledSide                 string
		ledPositionX            float64
		ledPositionY            float64
		ledCapacitorRotationInt int
		ledCapacitorSide        string
		ledCapacitorPositionX   float64
		ledCapacitorPositionY   float64
	)

	// Extract switch configuration settings
	switchRotationRaw, ok := settings["switchRotation"]
	if !ok {
		return "", ErrMissingSwitchRotation
	}
	switchRotation, ok := switchRotationRaw.(float64) // JSON numbers come as float64
	if !ok {
		return "", ErrInvalidSwitchRotation
	}
	switchRotationInt := int(switchRotation)

	switchSide, ok := settings["switchSide"].(string)
	if !ok {
		return "", ErrMissingSwitchSide
	}
	if switchSide != "FRONT" && switchSide != "BACK" {
		return "", ErrInvalidSwitchSide
	}

	// Extract diode configuration settings
	diodeRotationRaw, ok := settings["diodeRotation"]
	if !ok {
		return "", ErrMissingDiodeRotation
	}
	diodeRotation, ok := diodeRotationRaw.(float64) // JSON numbers come as float64
	if !ok {
		return "", ErrInvalidDiodeRotation
	}
	diodeRotationInt := int(diodeRotation)

	diodeSide, ok := settings["diodeSide"].(string)
	if !ok {
		return "", ErrMissingDiodeSide
	}
	if diodeSide != "FRONT" && diodeSide != "BACK" {
		return "", ErrInvalidDiodeSide
	}

	diodePositionX, ok := settings["diodePositionX"].(float64)
	if !ok {
		return "", ErrMissingDiodePositionX
	}

	diodePositionY, ok := settings["diodePositionY"].(float64)
	if !ok {
		return "", ErrMissingDiodePositionY
	}

	// Split footprint settings into library nickname and footprint name
	// Format: "lib_nickname:footprint"
	switchParts := strings.SplitN(switchFootprintSetting, ":", 2)
	if len(switchParts) != 2 {
		return "", fmt.Errorf("%w: switchFootprint must be in format 'lib:footprint'", ErrInvalidFootprintFormat)
	}
	switchLibNickname := switchParts[0]
	switchFp := switchParts[1]

	diodeParts := strings.SplitN(diodeFootprintSetting, ":", 2)
	if len(diodeParts) != 2 {
		return "", fmt.Errorf("%w: diodeFootprint must be in format 'lib:footprint'", ErrInvalidFootprintFormat)
	}
	diodeLibNickname := diodeParts[0]
	diodeFp := diodeParts[1]

	// Construct full footprint paths
	kicad3rdParty, err := GetKicad3rdPartyPath()
	switchFootprint := kicad3rdParty + SwitchesLibraryPath + switchLibNickname + ".pretty:" + switchFp
	diodeFootprint := DiodeLibraryPath + diodeLibNickname + ".pretty:" + diodeFp

	// Resolve stabilizer footprint path (same library as switches) when provided
	stabilizerFootprint := ""
	if stabilizerFootprintSetting != "" {
		stabParts := strings.SplitN(stabilizerFootprintSetting, ":", 2)
		if len(stabParts) != 2 {
			return "", fmt.Errorf("%w: stabilizerFootprint must be in format 'lib:footprint'", ErrInvalidFootprintFormat)
		}
		stabilizerFootprint = kicad3rdParty + SwitchesLibraryPath + stabParts[0] + ".pretty:" + stabParts[1]
	}

	// Resolve LED chain footprints (LED and decoupling capacitor live in the
	// standard KiCad footprint library, same as the diode).
	ledFootprint := ""
	ledCapacitorFootprint := ""
	if createLedSchFile {
		// LED footprint is always required: the PCB elements are generated
		// alongside the schematic and cannot be placed without it.
		if ledFootprintSetting == "" {
			return "", ErrMissingLedFootprint
		}
		ledParts := strings.SplitN(ledFootprintSetting, ":", 2)
		if len(ledParts) != 2 {
			return "", fmt.Errorf("%w: ledFootprint must be in format 'lib:footprint'", ErrInvalidFootprintFormat)
		}
		ledFootprint = DiodeLibraryPath + ledParts[0] + ".pretty:" + ledParts[1]

		// Decoupling capacitor is required unless decoupling is skipped.
		if !skipLedDecoupling {
			if ledCapacitorFootprintSetting == "" {
				return "", ErrMissingLedCapacitorFootprint
			}
			capParts := strings.SplitN(ledCapacitorFootprintSetting, ":", 2)
			if len(capParts) != 2 {
				return "", fmt.Errorf("%w: ledCapacitorFootprint must be in format 'lib:footprint'", ErrInvalidFootprintFormat)
			}
			ledCapacitorFootprint = DiodeLibraryPath + capParts[0] + ".pretty:" + capParts[1]
		}

		// LED placement is user-controlled: the footprint is positioned relative
		// to its key using the provided offset, rotation and side.
		ledRotationInt, ledSide, ledPositionX, ledPositionY, err = parsePlacement(settings, "led")
		if err != nil {
			return "", err
		}

		// The decoupling capacitor is only placed (and therefore only needs
		// positioning) when decoupling is not skipped.
		if !skipLedDecoupling {
			ledCapacitorRotationInt, ledCapacitorSide, ledCapacitorPositionX, ledCapacitorPositionY, err = parsePlacement(settings, "ledCapacitor")
			if err != nil {
				return "", err
			}
		}
	}

	fmt.Printf("switch_footprint=%s diode_footprint=%s stabilizer_footprint=%s\n", switchFootprint, diodeFootprint, stabilizerFootprint)

	// Determine routing options
	routeSwitchesWithDiodes := routing == "Switch-Diode only" || routing == "Full"
	routeRowsAndColumns := routing == "Full"

	// Create work directory
	workDir, err := CreateWorkDir(taskID)
	if err != nil {
		return "", err
	}

	// Get project name from layout metadata
	meta, ok := layout["meta"].(map[string]interface{})
	if !ok {
		return "", ErrInvalidLayoutMetadata
	}
	layoutName, _ := meta["name"].(string)
	projectName := GetProjectName(layoutName)

	// Create project directory
	projectFullPath, err := CreateKicadWorkDir(workDir, projectName)
	if err != nil {
		return "", err
	}

	// Create log directory
	logDir, err := CreateLogDir(workDir)
	if err != nil {
		return "", err
	}
	logPath := filepath.Join(logDir, "build.log")

	// Define file paths
	schFile := filepath.Join(projectFullPath, projectName+".kicad_sch")
	pcbFile := filepath.Join(projectFullPath, projectName+".kicad_pcb")
	layoutFile := filepath.Join(projectFullPath, projectName+".json")

	// Write layout JSON file
	layoutJSON, err := json.MarshalIndent(layout, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal layout JSON: %w", err)
	}
	if err := os.WriteFile(layoutFile, layoutJSON, 0644); err != nil {
		return "", fmt.Errorf("failed to write layout file: %w", err)
	}

	// Run kbplacer to generate PCB and schematic
	if err := RunKBPlacer(ctx, KBPlacerOptions{
		PCBPath:                 pcbFile,
		LayoutPath:              layoutFile,
		RouteSwitchesWithDiodes: routeSwitchesWithDiodes,
		RouteRowsAndColumns:     routeRowsAndColumns,
		SwitchFootprint:         switchFootprint,
		DiodeFootprint:          diodeFootprint,
		StabilizerFootprint:     stabilizerFootprint,
		SwitchRotation:          switchRotationInt,
		SwitchSide:              switchSide,
		DiodeRotation:           diodeRotationInt,
		DiodeSide:               diodeSide,
		DiodePositionX:          diodePositionX,
		DiodePositionY:          diodePositionY,
		CreateLedPcbElements:    createLedPcbElements,
		CreateLedSchFile:        createLedSchFile,
		LedFootprint:            ledFootprint,
		LedCapacitorFootprint:   ledCapacitorFootprint,
		SkipLedDecoupling:       skipLedDecoupling,
		LedRotation:             ledRotationInt,
		LedSide:                 ledSide,
		LedPositionX:            ledPositionX,
		LedPositionY:            ledPositionY,
		LedCapacitorRotation:    ledCapacitorRotationInt,
		LedCapacitorSide:        ledCapacitorSide,
		LedCapacitorPositionX:   ledCapacitorPositionX,
		LedCapacitorPositionY:   ledCapacitorPositionY,
	}, logPath); err != nil {
		return "", err
	}

	// Bundle switch footprints
	if err := BundleSwitchFootprints(projectFullPath, switchLibNickname); err != nil {
		return "", err
	}

	// Generate schematic image
	if err := GenerateSchematicImage(schFile, logPath); err != nil {
		return "", err
	}

	// Generate renders
	if err := GenerateRender(pcbFile, logPath); err != nil {
		return "", err
	}

	// Report duration time
	duration := time.Since(startTime)
	log.Printf("Task %s completed in %v", taskID, duration)

	return workDir, nil
}

// Helper function to copy a file
func copyFile(src, dst string) error {
	input, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	err = os.WriteFile(dst, input, 0644)
	if err != nil {
		return err
	}

	return nil
}

// Helper function to copy a directory recursively
func copyDir(src string, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Get relative path
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		targetPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(targetPath, info.Mode())
		}

		return copyFile(path, targetPath)
	})
}
