package archivehandler

import (
	"context"
	"log/slog"
	"time"

	"github.com/bevicted/lognav/internal/archive"
	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/logging"
)

// ArchiveCollectMsg is the collect-trigger emitted on Enter over a ready,
// not-expired archive. The root (Phase 8) handles it by calling
// instancepicker.StartCollect(Archive) and switching to the Instances/Logs tab.
// Defined here (component-local, like instancepicker's ArchiveDispatchedMsg) and
// emitted now; the root wiring lands in Phase 8.
type ArchiveCollectMsg struct {
	Archive *archive.Archive
}

// ArchivePollDoneMsg is posted by the poll worker when a manual poll of an
// in-progress archive finishes. The on-loop handler refreshes the list to recolor
// any instances whose state advanced.
type ArchivePollDoneMsg struct {
	Name string // full on-disk basename (incl. ext), as passed to pollArchive
}

// pollArchive manually polls each NON-terminal instance of an in-progress archive
// and rewrites the archive JSON with any advanced states. It is the Enter action
// for an in-progress archive, so it runs ON the loop goroutine: the prologue
// (strip ext, Load) is on-loop and only reads + spawns the off-loop worker. The
// worker does the network status fetches + the archive.Save (network-adjacent IO
// must stay off-loop, §13) and posts ArchivePollDoneMsg via PostCritical (a direct
// post is correct OFF-loop). Terminal instances are FROZEN and never re-polled.
func (m *Model) pollArchive(name string) {
	base := trimExt(name)
	p, err := archive.PathFor(base)
	if err != nil {
		m.logger.Warn("poll: path resolve failed", "name", base, logging.KeyError, err)
		return
	}
	a, err := archive.Load(p)
	if err != nil {
		m.logger.Warn("poll: archive load failed", "name", base, logging.KeyError, err)
		return
	}
	if m.resolver == nil {
		m.logger.Warn("poll: no token resolver wired; cannot poll", "name", base)
		return
	}
	resolve := m.resolver
	logger := m.logger
	m.poster.Go(func(ctx context.Context) {
		res := pollInstances(ctx, a, resolve, logger, config.EffectiveInstances(m.bundle.Config))
		// Whole-archive summary so a user diagnosing "Enter did nothing" can see
		// from the log whether the poll ran and what it found. Logged at WARN when
		// any instance errored (so it stands out), INFO otherwise.
		summarize := logger.Info
		if res.errors > 0 {
			summarize = logger.Warn
		}
		summarize("poll: archive polled", "name", base,
			"running", res.running, "advanced", res.advanced, "errors", res.errors)
		if res.advanced > 0 {
			if err := pollSaveFn(a); err != nil {
				logger.Error("poll: archive save failed", "name", base, logging.KeyError, err)
			}
		}
		_ = m.poster.PostCritical(ctx, ArchivePollDoneMsg{Name: name})
	})
}

// pollResult is the per-archive tally pollInstances returns: how many running
// instances it tried, how many advanced to a terminal state, and how many hit a
// resolver/status error (so the summary log can distinguish "all still running"
// from "every poll errored").
type pollResult struct {
	running  int // non-terminal instances examined this poll
	advanced int // instances whose state advanced (running -> terminal)
	errors   int // instances skipped due to a resolver or status error
}

// pollInstances fetches the live status of every non-terminal instance and folds
// it into a's per-instance state, returning a tally. Pure off-loop worker logic
// (extracted from pollArchive's closure to keep funlen/gocyclo low). Per-instance
// resolver/status errors are skipped (state preserved) but are LOGGED distinctly
// at WARN — they were previously swallowed silently, which made a stuck poll
// undiagnosable. Only an authoritative status advances the state.
func pollInstances(ctx context.Context, a *archive.Archive, resolve TokenResolver, logger *slog.Logger, configured []config.ICLInstanceConfig) pollResult {
	var res pollResult
	for i := range a.Instances {
		ie := &a.Instances[i]
		if ie.State != archive.StateRunning {
			continue // frozen terminal: never re-poll
		}
		res.running++
		displayName, nameErr := displayNameForCRN(configured, ie.CRN)
		if nameErr != nil {
			displayName = "invalid CRN"
		}
		token, url, err := resolve(ctx, ie.CRN)
		if err != nil {
			res.errors++
			logger.Warn("poll: token resolve failed; keeping prior state",
				logging.KeyInstance, displayName, logging.KeyError, err)
			continue // keep prior state; transient auth failure
		}
		st, err := pollStatusFn(ctx, token, url, ie.QueryID)
		if err != nil {
			res.errors++
			logger.Warn("poll: status fetch failed; keeping prior state",
				logging.KeyInstance, displayName, logging.KeyError, err)
			continue // keep prior state; transient status failure
		}
		ie.LastPolledAt = time.Now()
		if applyStatus(ie, st.State) {
			res.advanced++
		}
	}
	return res
}

// applyStatus maps an icl.BackgroundState onto an archive instance state, folding
// cancelled into error and not-found into expired (§13). On a non-success terminal
// it records a short ErrorMessage so the preview shows WHY the row is red — the
// status API gives no detailed reason, so the message is a fixed, concise summary.
// A success clears any prior message. Returns whether the state advanced (a running
// instance still running is no change).
func applyStatus(ie *archive.InstanceEntry, st icl.BackgroundState) bool {
	switch st {
	case icl.BackgroundSuccess:
		ie.State = archive.StateSuccess
		ie.ErrorMessage = ""
		return true
	case icl.BackgroundError:
		ie.State = archive.StateError
		ie.ErrorMessage = "query failed (or cancelled)"
		return true
	case icl.BackgroundNotFound:
		ie.State = archive.StateExpired
		ie.ErrorMessage = "expired — results no longer available"
		return true
	default: // icl.BackgroundRunning: still executing, no change
		return false
	}
}

// OnArchivePollDone is the on-loop handler for a finished poll: it drops the
// polled archive's CACHED preview, then refreshes the list. The filehandler caches
// previews in a basename-keyed map with no freshness check, and a poll rewrites the
// archive file IN PLACE (same name), so without invalidating first ListFiles would
// re-serve the stale preview and the RHS would keep showing the pre-poll states
// even though the row recolored. InvalidatePreview strips the ext itself, so the
// full basename in msg.Name is fine. The root dispatches it.
func (m *Model) OnArchivePollDone(msg ArchivePollDoneMsg) {
	m.fh.InvalidatePreview(msg.Name)
	m.fh.ListFiles()
}
