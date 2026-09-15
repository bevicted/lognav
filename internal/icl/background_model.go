package icl

// Background-query wire types. The background API (/api/v1/dataprime/background-query)
// uses TOP-LEVEL camelCase fields (startDate/endDate/syntax), distinct from the sync
// /v1/query path which nests snake_case dates under "metadata" (see model.go). The /data
// inner rows ALSO use camelCase "userData" (verified on ca-tor) — unlike sync's
// "user_data" — so a dedicated row type is required; DataprimeResults cannot be unmarshaled
// directly into.

// backgroundSubmitBody is POST /api/v1/dataprime/background-query.
type backgroundSubmitBody struct {
	Query     string `json:"query"`
	Syntax    string `json:"syntax"`    // always backgroundSyntaxDataprime
	StartDate string `json:"startDate"` // fallback window only; the query timeframe overrides
	EndDate   string `json:"endDate"`
}

const backgroundSyntaxDataprime = "QUERY_SYNTAX_DATAPRIME"

// backgroundSubmitResponse is the submit reply: {"queryId":"<uuid>","warnings":[]}.
type backgroundSubmitResponse struct {
	QueryID string `json:"queryId"`
}

// backgroundStatusWire decodes the status oneof:
//
//	{"running":{}} | {"terminated":{"success":{}|"cancelled":{}}, "submittedAt":...}
type backgroundStatusWire struct {
	Running     *struct{}             `json:"running,omitempty"`
	Terminated  *backgroundTerminated `json:"terminated,omitempty"`
	SubmittedAt string                `json:"submittedAt,omitempty"`
}

type backgroundTerminated struct {
	Success   *struct{} `json:"success,omitempty"`
	Cancelled *struct{} `json:"cancelled,omitempty"`
}

// BackgroundState is the decoded, UI-facing status of a single background query.
type BackgroundState int

const (
	BackgroundRunning  BackgroundState = iota // still executing server-side
	BackgroundSuccess                         // terminated successfully; data is fetchable
	BackgroundError                           // terminated non-success (incl. cancelled), or a decode/transport status error
	BackgroundNotFound                        // server reports the query id does not exist (expired/unknown)
)

// BackgroundStatus is the decoded GetBackgroundQueryStatus result.
type BackgroundStatus struct {
	State       BackgroundState
	SubmittedAt string // server-reported submit time, when present
}

// backgroundDataEnvelope decodes one NDJSON line of GET .../data:
//
//	{"response":{"results":{"results":[ <rows> ]}}}
type backgroundDataEnvelope struct {
	Response *backgroundDataResponse `json:"response,omitempty"`
}

type backgroundDataResponse struct {
	Results *backgroundDataResults `json:"results,omitempty"`
}

type backgroundDataResults struct {
	Results []backgroundRow `json:"results,omitempty"`
}

// backgroundRow is one result row from /data. NOTE the camelCase "userData" — this is the
// single field that differs from the sync DataprimeResults ("user_data"). labels/metadata/
// key/value have no underscores and decode identically.
type backgroundRow struct {
	Metadata []KeyValue `json:"metadata,omitempty"`
	Labels   []KeyValue `json:"labels,omitempty"`
	UserData *string    `json:"userData,omitempty"`
}

// toStreamItem maps a decoded /data envelope into the existing StreamItem shape so the
// unchanged DecodeStreamItem can consume it. Returns nil when there are no rows.
func (e *backgroundDataEnvelope) toStreamItem() *StreamItem {
	if e == nil || e.Response == nil || e.Response.Results == nil {
		return nil
	}
	src := e.Response.Results.Results
	if len(src) == 0 {
		return nil
	}
	rows := make([]DataprimeResults, len(src))
	for i := range src {
		rows[i] = DataprimeResults{Metadata: src[i].Metadata, Labels: src[i].Labels, UserData: src[i].UserData}
	}
	return &StreamItem{Result: &DataprimeResult{Results: rows}}
}
