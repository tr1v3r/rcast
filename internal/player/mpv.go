package player

// docs: https://mpv.io/manual/stable/#json-ipc

type MPVJSONIPCRequest struct {
	Command []any `json:"command"` // https://mpv.io/manual/stable/#properties

	RequestID int  `json:"request_id,omitempty"`
	Async     bool `json:"async,omitempty"`
}

type MPVJSONIPCResponse struct {
	RequestID int    `json:"request_id,omitempty"`
	Error     string `json:"error"`

	Data  any    `json:"data,omitempty"`
	Event string `json:"event,omitempty"`
	Name  string `json:"name,omitempty"`
}
