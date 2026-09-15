package icl

type (
	Priority uint8
	Severity uint8
)

const (
	SeverityUnknown Severity = iota
	SeverityVerbose
	SeverityDebug
	SeverityInfo
	SeverityWarning
	SeverityError
	SeverityCritical
)

func SeverityFromString(s string) Severity {
	switch s {
	case "Verbose":
		return SeverityVerbose
	case "Debug":
		return SeverityDebug
	case "Info":
		return SeverityInfo
	case "Warning":
		return SeverityWarning
	case "Error":
		return SeverityError
	case "Critical":
		return SeverityCritical
	default:
		return SeverityUnknown
	}
}

const (
	PriorityUnknown Priority = iota
	PriorityLow
	PriorityMedium
	PriorityHigh
)

func PriorityFromString(s string) Priority {
	switch s {
	case "low":
		return PriorityLow
	case "medium":
		return PriorityMedium
	case "high":
		return PriorityHigh
	default:
		return PriorityUnknown
	}
}

type Log struct {
	Data     map[string]any `json:"data"`
	Metadata Metadata       `json:"metadata"`
}

type Metadata struct {
	ID       string   `json:"id"`
	TSMicro  int64    `json:"ts_micro"`
	Severity Severity `json:"severity"`
	Priority Priority `json:"priority"`
}
