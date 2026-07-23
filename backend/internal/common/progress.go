package common

// Progress represents task progress information sent from worker to server via asynq/Redis.
// This struct must maintain binary compatibility for JSON serialization.
type Progress struct {
	Percentage int    `json:"percentage"`
	Message    string `json:"message,omitempty"`
	// Files is the manifest of generated artifacts. It is only populated on the
	// final (successful) result write, so intermediate progress updates stay
	// compact. See ProjectFiles.
	Files *ProjectFiles `json:"files,omitempty"`
}

// RenderFile describes a single SVG preview artifact generated for a task. The
// frontend fetches it (before downloading the result zip) via
// GET /api/pcb/{task_id}/render/{Name}.
type RenderFile struct {
	// Name is the render identifier used in the /render/{name} endpoint (e.g.
	// "front", "back", "schematic", "schematic-led-chain").
	Name string `json:"name"`
	// Kind categorizes the render: "pcb-front", "pcb-back" or "schematic".
	Kind string `json:"kind"`
	// Sheet is the schematic sheet name for multi-sheet projects (empty for the
	// root sheet and for PCB renders).
	Sheet string `json:"sheet,omitempty"`
}

// ProjectFiles is the manifest of artifacts produced by a completed task. It is
// returned in the final task result so the frontend can decide which previews to
// show and what to download without first unpacking the result zip.
type ProjectFiles struct {
	// Renders lists every SVG preview available through the /render endpoint.
	Renders []RenderFile `json:"renders"`
	// Archive is the file name of the downloadable project zip served by the
	// /result endpoint.
	Archive string `json:"archive"`
}
