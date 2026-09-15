package instancepicker

import (
	"errors"

	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/ui/components/logviewer"
)

// msg sent on instance select.
type InstanceSelectMsg struct {
	Name          string
	CRN           string
	Store         *logviewer.LogStore
	OpenLogViewer bool
	Line          int // optional scroll target, 0 = no scroll
}

// selectMsgFor builds the InstanceSelectMsg for r. The callers that additionally
// want OpenLogViewer or Line set that field on the returned value before emitting.
func selectMsgFor(r *Instance) InstanceSelectMsg {
	return InstanceSelectMsg{
		Name:  r.Name,
		CRN:   r.CRN,
		Store: r.Store,
	}
}

// InstanceFlushedMsg is sent when async log compression completes for an
// instance. epoch is the Model.fetchEpoch captured on-loop at flush start;
// OnInstanceFlushed drops the message when it no longer matches the current
// fetch, so a flush that outlived its fetch cannot touch the successor's
// backing container or flush accounting.
type InstanceFlushedMsg struct {
	crn           string
	compressed    []byte
	logsSizeBytes uint64
	err           error
	epoch         uint64
}

// InstanceLoadReadyMsg is the terminal signal from OpenInstance. Logs is
// non-nil on success. Err is non-nil if the load failed or the instance is
// not eligible (failed query, no backing file, fetch aborted).
type InstanceLoadReadyMsg struct {
	CRN  string
	Logs []icl.Log
	Err  error
}

// InstanceLoadPendingMsg signals that an OpenInstance request has been
// queued; the picker will dispatch InstanceLoadReadyMsg once the frame is
// durable. Consumers may use this to show a "loading…" indicator.
type InstanceLoadPendingMsg struct {
	CRN string
}

var (
	ErrNoBackingFile  = errors.New("no backing file for lazy load")
	ErrInstanceFailed = errors.New("instance query failed or is disabled")
	ErrFetchAborted   = errors.New("fetch aborted before frame was durable")
	// errCollectBusy is returned by startCollect when a fetch or watch is already
	// running (collect is a peer fetch mode and must not interleave with one).
	errCollectBusy = errors.New("cannot collect while a fetch or watch is running")
)
