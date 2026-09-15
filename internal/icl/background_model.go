package icl

// backgroundSubmitBody is the public POST /v1/background_query request.
type backgroundSubmitBody struct {
	Query     string `json:"query"`
	Syntax    string `json:"syntax"`
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
}

const backgroundSyntaxDataprime = "dataprime"

// backgroundSubmitResponse is the submit reply.
type backgroundSubmitResponse struct {
	QueryID string `json:"query_id"`
}

// backgroundStatusWire decodes the public status response oneof.
type backgroundStatusWire struct {
	Running             *struct{}             `json:"running,omitempty"`
	Terminated          *backgroundTerminated `json:"terminated,omitempty"`
	WaitingForExecution *struct{}             `json:"waiting_for_execution,omitempty"`
	SubmittedAt         string                `json:"submitted_at,omitempty"`
}

type backgroundTerminated struct {
	Success   *struct{} `json:"success,omitempty"`
	Error     *struct{} `json:"error,omitempty"`
	Cancelled *struct{} `json:"cancelled,omitempty"`
}

// BackgroundState is the decoded, UI-facing status of a single background query.
type BackgroundState int

const (
	BackgroundRunning  BackgroundState = iota // waiting or executing server-side
	BackgroundSuccess                         // terminated successfully; data is fetchable
	BackgroundError                           // terminated with an error or cancellation
	BackgroundNotFound                        // server reports the query ID does not exist
)

// BackgroundStatus is the decoded GetBackgroundQueryStatus result.
type BackgroundStatus struct {
	State       BackgroundState
	SubmittedAt string // server-reported submit time, when present
}

// backgroundDataEnvelope decodes one SSE data event from the public data endpoint.
type backgroundDataEnvelope struct {
	Response *backgroundDataResponse `json:"response,omitempty"`
}

type backgroundDataResponse struct {
	Results *DataprimeResult `json:"results,omitempty"`
}

// toStreamItem maps a background data event into the existing synchronous stream shape.
func (e *backgroundDataEnvelope) toStreamItem() *StreamItem {
	if e == nil || e.Response == nil || e.Response.Results == nil {
		return nil
	}
	return &StreamItem{Result: e.Response.Results}
}
