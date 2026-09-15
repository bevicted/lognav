package msgs

type ViewInContextMsg struct {
	CRN       string
	PodID     string
	LogID     string
	Timestamp int64
}
