package kicad

import "errors"

// Validation errors - these indicate bad input
var (
	ErrInvalidLayout          = errors.New("invalid layout in task request")
	ErrInvalidSettings        = errors.New("invalid settings in task request")
	ErrInvalidFootprintFormat = errors.New("invalid footprint format")
	ErrInvalidLayoutMetadata  = errors.New("invalid layout metadata")

	// Switch configuration errors
	ErrInvalidSwitchRotation = errors.New("invalid switchRotation: must be an integer")
	ErrInvalidSwitchSide     = errors.New("invalid switchSide: must be FRONT or BACK")
	ErrMissingSwitchRotation = errors.New("missing required field: switchRotation")
	ErrMissingSwitchSide     = errors.New("missing required field: switchSide")

	// Diode configuration errors
	ErrInvalidDiodeRotation  = errors.New("invalid diodeRotation: must be an integer")
	ErrInvalidDiodeSide      = errors.New("invalid diodeSide: must be FRONT or BACK")
	ErrMissingDiodeRotation  = errors.New("missing required field: diodeRotation")
	ErrMissingDiodeSide      = errors.New("missing required field: diodeSide")
	ErrMissingDiodePositionX = errors.New("missing required field: diodePositionX")
	ErrMissingDiodePositionY = errors.New("missing required field: diodePositionY")

	// LED chain configuration errors
	ErrMissingLedFootprint          = errors.New("missing required field: ledFootprint (required when createLedSchFile is enabled)")
	ErrMissingLedCapacitorFootprint = errors.New("missing required field: ledCapacitorFootprint (required when createLedSchFile is enabled unless skipLedDecoupling is set)")

	// LED chain placement errors (LED and decoupling capacitor positioning).
	// The offending field name is appended when the error is raised.
	ErrMissingLedPlacement = errors.New("missing required placement field")
	ErrInvalidLedPlacement = errors.New("invalid placement field")
)
