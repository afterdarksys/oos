package guard

// OpenFile is one open path and the process holding it.
type OpenFile struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
	Path    string `json:"path"`
}
