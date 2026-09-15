package icl

import "time"

// StreamItem is one decoded SSE "data:" event from the /v1/query stream. Only
// the fields lognav consumes are modeled; unknown JSON keys are ignored on
// decode. Pointer fields mirror the retired SDK's shape so decode.go's nil
// checks stay mechanical.
type StreamItem struct {
	Error   *DataprimeError   `json:"error,omitempty"`
	Result  *DataprimeResult  `json:"result,omitempty"`
	Warning *DataprimeWarning `json:"warning,omitempty"`
	Errors  []APIError        `json:"errors,omitempty"`
}

// DataprimeError is the single-error variant of a stream item.
type DataprimeError struct {
	Message *string `json:"message,omitempty"`
}

// APIError mirrors the retired SDK's OpenapiApiErrorError ("errors" array).
type APIError struct {
	Code     *string `json:"code,omitempty"`
	Message  *string `json:"message,omitempty"`
	MoreInfo *string `json:"more_info,omitempty"`
}

// DataprimeResult holds the batch of result rows in a result stream item.
type DataprimeResult struct {
	Results []DataprimeResults `json:"results,omitempty"`
}

// DataprimeResults is one log row: its user data plus label/metadata fields.
type DataprimeResults struct {
	Metadata []KeyValue `json:"metadata,omitempty"`
	Labels   []KeyValue `json:"labels,omitempty"`
	UserData *string    `json:"user_data,omitempty"`
}

// KeyValue is a single label or metadata field.
type KeyValue struct {
	Key   *string `json:"key,omitempty"`
	Value *string `json:"value,omitempty"`
}

// DataprimeWarning is the warning variant. The retired SDK modeled this as a
// oneof interface; lognav reads only the time-range branch, so a concrete
// struct suffices and lets the whole event decode in a single unmarshal.
type DataprimeWarning struct {
	TimeRangeWarning *TimeRangeWarning `json:"time_range_warning,omitempty"`
}

// TimeRangeWarning carries the warning message shown to the user.
type TimeRangeWarning struct {
	WarningMessage *string `json:"warning_message,omitempty"`
}

// QueryMetadata is the request-body "metadata" block for a Dataprime query.
// (Named QueryMetadata, not Metadata — the latter is the existing log-metadata
// type in types.go.) StartDate/EndDate are pre-formatted strings (see
// formatICLTime) so the package no longer depends on go-openapi/strfmt.
//
// Every field is always sent, so all tags are non-omitempty: the request body
// is a fixed shape, and a value type with no omitempty cannot silently drop a
// key the server expects.
type QueryMetadata struct {
	DefaultSource string `json:"default_source"`
	StartDate     string `json:"start_date"`
	EndDate       string `json:"end_date"`
	Syntax        string `json:"syntax"`
	Tier          string `json:"tier"`
	Limit         int64  `json:"limit"`
}

// deref returns the pointed-to string, or "" if the pointer is nil. Replaces
// the retired go-sdk-core core.StringNilMapper.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// iclTimeLayout is RFC3339 with milliseconds. The retired SDK marshaled query
// dates via strfmt.DateTime, which emits this layout forced to UTC;
// formatICLTime reproduces it exactly so request bytes are unchanged.
const iclTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// formatICLTime renders t as the ICL query date format: RFC3339-millis in UTC.
func formatICLTime(t time.Time) string {
	return t.UTC().Format(iclTimeLayout)
}
