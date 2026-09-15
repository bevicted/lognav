package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/externaleditor"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/querytemplate"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/ui/status"
)

var (
	queryStream      = icl.Query
	queryStreamStart = func(ctx context.Context, token, url, query string, maxRows uint32, cb icl.QueryCallback, started func()) error {
		started()
		return queryStream(ctx, token, url, query, maxRows, cb)
	}
	queryURL                 = icl.GetURLFromCRN
	queryNow                 = time.Now
	newQueryAccountManager   = icl.NewAccountManager
	querySnapshotDir         = snapshot.Dir
	queryRetainSnapshots     = snapshot.RetainAutoSnapshots
	queryPublishAutoSnapshot = snapshot.PublishAutoSnapshot
	querySnapshotCheckpoint  = func(string) {}
	queryOutcomeCommitted    = func() {}
	queryInputReadStarted    = func() {}
	resolveQueryToken        = func(ctx context.Context, manager *icl.AccountManager, crn *config.CRN) (string, error) {
		return manager.GetAuthTokenNoPasscode(ctx, crn)
	}
)

// initQuery builds the blocking headless query command. Selectors are resolved
// as a complete set before stdin/editor side effects so a typo cannot start a
// fetch.
func initQuery(loadBundle func(*cobra.Command) (deps.Bundle, error)) *cobra.Command {
	var all, first, tee bool
	var selectors []string
	c := &cobra.Command{
		Use:     "query",
		Short:   "Run a Dataprime query and save its logs as a snapshot",
		Long:    "Run a Dataprime query without starting the TUI. Effective configured instances run before explicit instance values, which retain flag order; configured names and full ICL CRNs that resolve to the same CRN run once. With `--first` and no other selector, all effective configured instances are selected. At least one target is required. All selectors are validated before input is read or authentication starts.\n\nRedirected stdin is sent verbatim, including an empty query. Terminal stdin opens $EDITOR, falling back to vim, seeded with the resolved `icl.defaultQuery` and, when enabled, resolved snippets; simple whitespace-separated editor arguments are supported, but shell syntax is not.\n\nWithout `--tee`, stdout contains only the retained snapshot selector when logs were saved. Automatic retention runs before selector output, so stdout is empty when it does not retain the snapshot. Capture it before checking status: a partial result can print a selector and still fail. `--tee` records have no instance wrapper or selector; records from concurrent targets can interleave, writes synchronously backpressure a slow consumer, and a stdout write failure stops the query without a finalized snapshot. A later target failure can leave valid partial NDJSON already consumed. In `--first` mode, only the winner emits records.\n\nProgress, member diagnostics, and the final snapshot outcome go to stderr. Redirected stderr has finite plain output without ANSI control sequences. `--tee` suppresses progress but retains diagnostics and the final outcome on stderr. A successful all-empty result prints `0 logs; no snapshot created` to stderr and no selector. A retained snapshot includes every selected target, including failed and zero-result states. `--first` also records intentionally canceled losers. Cancellation leaves no finalized snapshot.\n\nExit status: 0 when all members complete, including zero logs; 1 for mixed or other failures; 65 when every member rejects the Dataprime query; 69 when every member is unavailable or cannot authenticate. In `--first` mode, the winner alone determines diagnostics and exit status. Headless query never prompts for a passcode.",
		Example: "  printf '%s\\n' 'source logs last 5m' | lognav query --instance eu-de --instance us-south\n  lognav query --all\n  printf '%s\\n' 'source logs last 5m' | lognav query --tee --all | jq .\n  selector=$(printf '%s\\n' \"$query\" | lognav query --instance eu-de) || query_status=$?",
		Args:    noQueryArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			bundle, err := loadBundle(cmd)
			if err != nil {
				return err
			}
			return runQueryHandler(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), bundle.Config, queryHandlerOptions{
				all:       all,
				first:     first,
				selectors: selectors,
				tee:       tee,
			})
		},
	}
	c.ValidArgsFunction = cobra.NoFileCompletions
	c.Flags().BoolVarP(&all, "all", "a", false, "query all effective configured instances")
	c.Flags().BoolVarP(&first, "first", "f", false, "race targets until the first decoded log arrives")
	c.Flags().StringArrayVarP(&selectors, "instance", "i", nil, "query an instance by configured name or full ICL CRN (repeatable)")
	c.Flags().BoolVarP(&tee, "tee", "t", false, "stream native NDJSON logs to stdout while saving a snapshot")
	_ = c.RegisterFlagCompletionFunc("instance", completeQueryInstance(loadBundle))
	return c
}

// queryHandlerOptions holds the syntax-derived options for one headless query.
type queryHandlerOptions struct {
	all       bool
	first     bool
	selectors []string
	tee       bool
}

// runQueryHandler resolves selectors before reading input, then runs the query.
func runQueryHandler(ctx context.Context, in io.Reader, stdout, stderr io.Writer, cfg *config.Config, options queryHandlerOptions) error {
	selected, err := selectedQueryInstances(cfg, options.all || (options.first && len(options.selectors) == 0), options.selectors)
	if err != nil {
		return err
	}
	query, err := readQueryInput(ctxOrBackground(ctx), in, cfg)
	if err != nil {
		return err
	}
	return runQueryWithOptions(ctxOrBackground(ctx), stdout, stderr, cfg, selected, query, queryRunOptions{first: options.first, tee: options.tee})
}

// noQueryArgs reserves query text for stdin or the external editor.
func noQueryArgs(_ *cobra.Command, args []string) error {
	if len(args) != 0 {
		return WithExit(ExitUsage, errors.New("query accepts no positional arguments"))
	}
	return nil
}

// ctxOrBackground protects direct command tests that do not attach a context.
func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// selectedQueryInstances resolves --all before repeated --instance selectors
// and deduplicates their full CRNs. It does no input, authentication, or query I/O.
func selectedQueryInstances(cfg *config.Config, all bool, selectors []string) ([]config.ICLInstanceConfig, error) {
	if !all && len(selectors) == 0 {
		return nil, WithExit(ExitUsage, errors.New("at least one target selector is required"))
	}

	effective := config.EffectiveInstances(cfg)
	known := make(map[string]config.ICLInstanceConfig, len(effective))
	for _, instance := range effective {
		known[instance.Name] = instance
	}

	selected := make([]config.ICLInstanceConfig, 0, len(effective)+len(selectors))
	seen := make(map[string]struct{}, cap(selected))
	appendInstance := func(instance config.ICLInstanceConfig) {
		if _, ok := seen[instance.CRN.String()]; ok {
			return
		}
		seen[instance.CRN.String()] = struct{}{}
		selected = append(selected, instance)
	}
	if all {
		for _, instance := range effective {
			appendInstance(instance)
		}
	}
	for _, selector := range selectors {
		if instance, ok := known[selector]; ok {
			appendInstance(instance)
			continue
		}
		crn, err := config.CRNFromString(selector)
		if err != nil {
			return nil, WithExit(ExitUsage, fmt.Errorf("instance %q is not a configured name or valid ICL CRN: %w", selector, err))
		}
		appendInstance(config.ICLInstanceConfig{
			Name: config.DisplayNameForCRN(effective, crn),
			CRN:  crn,
		})
	}
	if len(selected) == 0 {
		return nil, WithExit(ExitUsage, errors.New("no query targets resolved"))
	}
	for _, instance := range selected {
		if _, ok := cfg.ICL.Environments[instance.CRN.CName]; !ok {
			return nil, WithExit(ExitUsage, fmt.Errorf("instance %q uses unconfigured ICL environment %q", instance.Name, instance.CRN.CName))
		}
	}
	return selected, nil
}

func completeQueryInstance(loadBundle func(*cobra.Command) (deps.Bundle, error)) cobra.CompletionFunc {
	return func(cmd *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
		bundle, err := loadBundle(cmd)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var names []string
		for _, instance := range config.EffectiveInstances(bundle.Config) {
			if strings.HasPrefix(instance.Name, prefix) {
				names = append(names, instance.Name)
			}
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

// readQueryInput reads redirected stdin exactly as supplied. A terminal opens
// the shared editor path used by the TUI, including its snippet reference and
// vim fallback. Empty input from either source is a valid Dataprime query.
func readQueryInput(ctx context.Context, in io.Reader, cfg *config.Config) (string, error) {
	if !isTTY() {
		data, err := readQueryStdin(ctx, in)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	return readQueryInputAt(ctx, in, cfg, time.Now())
}

// readQueryInputAt resolves terminal-editor startup content at one explicit
// instant. Redirected input remains outside this path and is never templated.
func readQueryInputAt(ctx context.Context, _ io.Reader, cfg *config.Config, now time.Time) (string, error) {
	content, err := querytemplate.Resolve(cfg, now)
	if err != nil {
		return "", fmt.Errorf("resolve startup query templates: %w", err)
	}

	command, finish, err := externaleditor.Prepare(ctx, content.DefaultQuery, cfg.Core.IncludeSnippetsInEditor, content.Snippets)
	if err != nil {
		return "", fmt.Errorf("prepare query editor: %w", err)
	}
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		_, _ = finish()
		return "", fmt.Errorf("query editor: %w", err)
	}
	query, err := finish()
	if err != nil {
		return "", fmt.Errorf("read query editor result: %w", err)
	}
	return query, nil
}

// runOneQuery retains the one-member seam used by callers and tests.
func runOneQuery(ctx context.Context, stdout, stderr io.Writer, cfg *config.Config, instance config.ICLInstanceConfig, query string) error {
	return runQuery(ctx, stdout, stderr, cfg, []config.ICLInstanceConfig{instance}, query)
}

type queryRunOptions struct {
	first bool
	tee   bool
}

// queryTeeEmitter serializes native NDJSON writes from concurrent stream
// callbacks. Its synchronous Write path intentionally propagates slow-consumer
// backpressure to the callback. The first emission error cancels every worker.
type queryTeeEmitter struct {
	mu     sync.Mutex
	emitFn func(icl.Log) error
	cancel context.CancelFunc
	err    error
}

func newQueryTeeEmitter(emit func(icl.Log) error, cancel context.CancelFunc) *queryTeeEmitter {
	return &queryTeeEmitter{emitFn: emit, cancel: cancel}
}

func (e *queryTeeEmitter) emit(log icl.Log) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return e.err
	}
	if err := e.emitFn(log); err != nil {
		e.err = err
		e.cancel()
		return err
	}
	return nil
}

func (e *queryTeeEmitter) outputErr() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

func (e *queryTeeEmitter) outputError() error {
	return WithExit(ExitGeneral, fmt.Errorf("write live query output: %w", e.outputErr()))
}

// runQuery retains the ordinary selector-only stdout behavior used by callers
// and tests. --tee is deliberately opt-in through runQueryWithOptions.
func runQuery(ctx context.Context, stdout, stderr io.Writer, cfg *config.Config, instances []config.ICLInstanceConfig, query string) error {
	return runQueryWithOptions(ctx, stdout, stderr, cfg, instances, query, queryRunOptions{})
}

// runQueryWithOptions authenticates members sequentially within each environment
// and in parallel across environments. A successful auth starts its stream before
// the next same-environment member is resolved. It joins every worker before a
// snapshot is considered, so cancellation cannot leave stream goroutines behind.
//
//nolint:gocyclo,funlen // Auth, joining, publication, and exit ordering are one command contract.
func runQueryWithOptions(ctx context.Context, stdout, stderr io.Writer, cfg *config.Config, instances []config.ICLInstanceConfig, query string, options queryRunOptions) error {
	ctx = ctxOrBackground(ctx)
	if len(instances) == 0 {
		return WithExit(ExitUsage, errors.New("at least one configured instance is required"))
	}

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	authCtx, cancelAuth := context.WithCancel(workCtx)
	defer cancelAuth()
	var tee *queryTeeEmitter
	if options.tee {
		tee = newQueryTeeEmitter(logEmitter(stdout), cancel)
	}
	manager := newQueryAccountManager(queryEnvironments(cfg))
	initialTokens, sessionPath, err := loadQuerySession(manager)
	if err != nil {
		fmt.Fprintln(stderr, "no snapshot created")
		return WithExit(ExitGeneral, err)
	}

	now := queryNow()
	members := make([]queryMember, len(instances))
	groups := map[icl.Environment][]int{}
	var environmentOrder []icl.Environment
	for idx, instance := range instances {
		members[idx] = newQueryMember(instance, now)
		members[idx].tee = tee
		env := icl.Environment(instance.CRN.CName)
		if _, seen := groups[env]; !seen {
			environmentOrder = append(environmentOrder, env)
		}
		groups[env] = append(groups[env], idx)
	}
	var first *queryFirstRace
	if options.first {
		first = newQueryFirstRace(workCtx, cancelAuth, members)
		for index := range members {
			members[index].first, members[index].index = first, index
		}
	}
	reporter := newQueryProgressReporter(stderr, cfg, members, options.tee)
	reporter.Start()

	var authWG, streamWG sync.WaitGroup
	for _, env := range environmentOrder {
		indexes := groups[env]
		if len(indexes) == 0 {
			continue
		}
		authWG.Add(1)
		go func(indexes []int) {
			defer authWG.Done()
			resolveQueryEnvironment(authCtx, workCtx, manager, members, indexes, query, cfg.Logs.MaxRows, &streamWG, first)
		}(indexes)
	}
	authWG.Wait()
	if tee != nil && tee.outputErr() != nil {
		streamWG.Wait()
		finishQueryReporter(reporter)
		return reportQueryOutputFailure(reporter, tee)
	}

	// A rotated refresh token is shared state from auth, so persist only after
	// both environment workers have finished mutating it.
	if err := saveRotatedQuerySession(sessionPath, initialTokens, manager.GetRefreshTokens()); err != nil {
		cancel()
		streamWG.Wait()
		finishQueryReporter(reporter)
		if tee != nil && tee.outputErr() != nil {
			return reportQueryOutputFailure(reporter, tee)
		}
		reporter.SnapshotOutcome("no snapshot created")
		return WithExit(ExitGeneral, err)
	}
	streamWG.Wait()
	finishQueryReporter(reporter)
	if tee != nil && tee.outputErr() != nil {
		return reportQueryOutputFailure(reporter, tee)
	}
	if err := ctx.Err(); err != nil {
		reporter.SnapshotOutcome("query cancelled; no snapshot created")
		return err
	}

	if !queryMembersHaveLogs(members) {
		reporter.WriteMemberIssues()
		reporter.SnapshotOutcome("0 logs; no snapshot created")
		return queryMembersError(members)
	}
	outcomeMembers := members
	if first != nil {
		winner := first.winnerIndex()
		if winner < 0 {
			reporter.WriteMemberIssues()
			reporter.SnapshotOutcome("0 logs; no snapshot created")
			return queryMembersError(members)
		}
		outcomeMembers = []queryMember{members[winner]}
	}
	reporter.WriteMemberIssuesFor(outcomeMembers)

	selector, retained, finalPath, snapshotErr := finalizeQuerySnapshot(ctx, stderr, cfg, members, query, now)
	if ctx.Err() != nil {
		_ = os.Remove(finalPath)
		reporter.SnapshotOutcome("query cancelled; no snapshot created")
		return ctx.Err()
	}
	cleanupPublished := publishedCleanupError(finalPath, snapshotErr)
	if snapshotErr != nil && !cleanupPublished {
		reporter.SnapshotOutcome("snapshot creation failed")
		return WithExit(ExitGeneral, snapshotErr)
	}
	if !retained {
		if cleanupPublished {
			reporter.SnapshotOutcome("snapshot created but not retained; WIP cleanup failed: %v", snapshotErr)
			return WithExit(ExitGeneral, snapshotErr)
		}
		reporter.SnapshotOutcome("snapshot created but not retained")
		return queryMembersError(outcomeMembers)
	}
	// A retained tee snapshot, like an emitted ordinary selector, is a durable
	// success outcome. This commit linearizes signal cleanup against either
	// outcome: a signal that wins first removes the snapshot; a later signal
	// cannot turn the retained result into exit 130.
	if !commitQueryOutcome(ctx) {
		_ = os.Remove(finalPath)
		reporter.SnapshotOutcome("query cancelled; no snapshot created")
		return ctx.Err()
	}
	queryOutcomeCommitted()
	if options.tee {
		if cleanupPublished {
			reporter.SnapshotOutcome("%d logs; snapshot retained; WIP cleanup failed: %v", queryMemberLogCount(outcomeMembers), snapshotErr)
			return WithExit(ExitGeneral, snapshotErr)
		}
		reporter.SnapshotOutcome("%d logs; snapshot retained", queryMemberLogCount(outcomeMembers))
		return queryMembersError(outcomeMembers)
	}
	if _, err := fmt.Fprintln(stdout, selector); err != nil {
		reporter.SnapshotOutcome("snapshot selector write failed")
		return WithExit(ExitGeneral, fmt.Errorf("write snapshot selector: %w", err))
	}
	if cleanupPublished {
		reporter.SnapshotOutcome("%d logs; snapshot %s; WIP cleanup failed: %v", queryMemberLogCount(outcomeMembers), selector, snapshotErr)
		return WithExit(ExitGeneral, snapshotErr)
	}
	reporter.SnapshotOutcome("%d logs; snapshot %s", queryMemberLogCount(outcomeMembers), selector)
	return queryMembersError(outcomeMembers)
}

func finishQueryReporter(reporter *queryProgressReporter) {
	reporter.Finish()
	reporter.WriteTransitions()
	reporter.WriteFinalTable()
}

func reportQueryOutputFailure(reporter *queryProgressReporter, tee *queryTeeEmitter) error {
	reporter.SnapshotOutcome("live output failed; no snapshot created")
	return tee.outputError()
}

// resolveQueryEnvironment applies the TUI shared-credential rule: a first
// member auth failure marks its remaining same-environment members failed; a
// later failure affects only that member. This worker is intentionally one per
// environment, preserving sequential same-environment token resolution.
func resolveQueryEnvironment(authCtx, streamParent context.Context, manager *icl.AccountManager, members []queryMember, indexes []int, query string, maxRows uint32, streamWG *sync.WaitGroup, first *queryFirstRace) {
	for position, index := range indexes {
		token, err := resolveQueryToken(authCtx, manager, members[index].instance.CRN)
		if err != nil {
			if authCtx.Err() != nil {
				return
			}
			if position == 0 {
				for _, pending := range indexes[position:] {
					members[pending].authFailed(err)
				}
				return
			}
			members[index].authFailed(err)
			continue
		}

		streamCtx := streamParent
		if first != nil {
			var start bool
			streamCtx, start = first.streamContext(index)
			if !start {
				return
			}
		} else {
			members[index].startQuery()
		}
		streamWG.Add(1)
		started := make(chan struct{})
		go func(index int, token string, streamCtx context.Context) {
			defer streamWG.Done()
			member := &members[index]
			err := queryStreamStart(streamCtx, token, queryURL(member.instance.CRN), query, maxRows, member, func() { close(started) })
			if err != nil && streamCtx.Err() == nil && (first == nil || first.accepts(index)) {
				member.failed(err, classifyQueryError(err))
			}
		}(index, token, streamCtx)
		// The next same-environment credential resolution may share mutable auth
		// state, but only after this member's stream worker has begun.
		<-started
	}
}

// queryFirstRace atomically chooses the first member with decoded logs. Its
// authentication context is separate from member stream contexts so the winner
// remains live after pending authentication and loser streams are canceled.
type queryFirstRace struct {
	mu            sync.Mutex
	parent        context.Context
	authCancel    context.CancelFunc
	members       []queryMember
	streamCancels []context.CancelFunc
	winner        int
}

func newQueryFirstRace(parent context.Context, authCancel context.CancelFunc, members []queryMember) *queryFirstRace {
	return &queryFirstRace{parent: parent, authCancel: authCancel, members: members, streamCancels: make([]context.CancelFunc, len(members)), winner: -1}
}

func (r *queryFirstRace) winnerIndex() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.winner
}

func (r *queryFirstRace) accepts(index int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.winner < 0 || r.winner == index
}

// streamContext registers and marks a member started while winner election is
// excluded, so a canceled loser cannot be restarted after this method returns.
func (r *queryFirstRace) streamContext(index int) (context.Context, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.winner >= 0 {
		return nil, false
	}
	ctx, cancel := context.WithCancel(r.parent)
	r.streamCancels[index] = cancel
	r.members[index].startQuery()
	return ctx, true
}

// acceptData elects the first member with actual logs. Empty, warning-only,
// and error-only callbacks remain candidate state but cannot elect a winner.
func (r *queryFirstRace) acceptData(index, logCount int) bool {
	r.mu.Lock()
	if r.winner >= 0 {
		winner := r.winner
		r.mu.Unlock()
		return winner == index
	}
	if logCount == 0 {
		r.mu.Unlock()
		return true
	}
	r.winner = index
	cancels := append([]context.CancelFunc(nil), r.streamCancels...)
	members := r.members
	authCancel := r.authCancel
	r.mu.Unlock()

	authCancel()
	for loser, cancel := range cancels {
		if loser == index {
			continue
		}
		members[loser].cancelled()
		if cancel != nil {
			cancel()
		}
	}
	return true
}

type queryFailure int

const (
	queryFailureNone queryFailure = iota
	queryFailureData
	queryFailureUnavailable
	queryFailureGeneral
)

type queryMember struct {
	instance config.ICLInstanceConfig
	first    *queryFirstRace
	index    int

	mu      *sync.RWMutex
	phase   status.Phase
	started time.Time
	updated time.Time
	history []status.Phase
	result  queryResult
	tee     *queryTeeEmitter
}

func newQueryMember(instance config.ICLInstanceConfig, now time.Time) queryMember {
	return queryMember{instance: instance, mu: &sync.RWMutex{}, phase: status.AuthInProgress, started: now, updated: now, history: []status.Phase{status.AuthInProgress}, result: queryResult{mu: &sync.RWMutex{}}}
}

func (m *queryMember) setPhase(phase status.Phase, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.phase != phase {
		m.phase = phase
		m.history = append(m.history, phase)
	}
	m.updated = now
}

func (m *queryMember) cancelled() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.phase == status.AuthInProgress || m.phase == status.InProgress {
		m.phase = status.Cancelled
		m.history = append(m.history, status.Cancelled)
		m.updated = queryNow()
	}
}

func (m *queryMember) startQuery() {
	now := queryNow()
	m.mu.Lock()
	m.phase, m.started, m.updated = status.InProgress, now, now
	m.history = append(m.history, status.InProgress)
	m.mu.Unlock()
}

// setStreamPhase mirrors the picker: a stream warning/error changes the badge
// immediately, but its timer remains frozen at the last terminal/start value
// until the stream closes.
func (m *queryMember) setStreamPhase(phase status.Phase) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.phase != phase {
		m.phase = phase
		m.history = append(m.history, phase)
	}
}

func (m *queryMember) authFailed(err error) {
	m.failed(err, queryFailureUnavailable)
}

func (m *queryMember) stateAndMessage() (status.Phase, string) {
	result := m.resultSnapshot()
	m.mu.RLock()
	phase := m.phase
	m.mu.RUnlock()
	if result.err != nil {
		return status.Error, result.err.Error()
	}
	if len(result.warnings) > 0 {
		return status.Warning, strings.Join(result.warnings, "\n")
	}
	return phase, ""
}

func (m *queryMember) view() queryMemberView {
	result := m.resultSnapshot()
	m.mu.RLock()
	view := queryMemberView{Name: m.instance.Name, Phase: m.phase, Count: len(result.logs), Started: m.started, Updated: m.updated, Live: m.phase == status.AuthInProgress || m.phase == status.InProgress}
	m.mu.RUnlock()
	return view
}

func (m *queryMember) phaseHistory() []status.Phase {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]status.Phase(nil), m.history...)
}

func queryMembersHaveLogs(members []queryMember) bool {
	for i := range members {
		if len(members[i].resultSnapshot().logs) > 0 {
			return true
		}
	}
	return false
}

func queryMemberLogCount(members []queryMember) int {
	count := 0
	for i := range members {
		count += len(members[i].resultSnapshot().logs)
	}
	return count
}

// queryMembersError implements the shell contract after every selected member
// settled. Warnings are successful query outcomes; a successful zero result is
// also successful. The special data/unavailable exits require every member to
// have failed in that class, otherwise partial and unclassified outcomes are 1.
func queryMembersError(members []queryMember) error {
	allData, allUnavailable := true, true
	anyFailure := false
	var causes []error
	for i := range members {
		member := &members[i]
		result := member.resultSnapshot()
		switch result.failure {
		case queryFailureNone:
			allData, allUnavailable = false, false
		case queryFailureData:
			anyFailure = true
			allUnavailable = false
		case queryFailureUnavailable:
			anyFailure = true
			allData = false
		default:
			anyFailure = true
			allData, allUnavailable = false, false
		}
		if result.err != nil {
			causes = append(causes, fmt.Errorf("query %s: %w", member.instance.Name, result.err))
		}
	}
	if !anyFailure {
		return nil
	}
	code := ExitGeneral
	if allData {
		code = ExitData
	} else if allUnavailable {
		code = ExitUnavailable
	}
	return WithExit(code, errors.Join(causes...))
}

func apiKeyFromEnv(configKey string) string {
	if key := os.Getenv("LOGNAV_IC_API_KEY"); key != "" {
		return key
	}
	return configKey
}

// queryEnvironments copies configured IAM records and applies the legacy
// production-only environment override without affecting other environments.
func queryEnvironments(cfg *config.Config) map[string]config.ICLEnvironmentConfig {
	environments := maps.Clone(cfg.ICL.Environments)
	if production, ok := environments[string(icl.EnvProd)]; ok {
		production.APIKey = apiKeyFromEnv(production.APIKey)
		environments[string(icl.EnvProd)] = production
	}
	return environments
}

func loadQuerySession(manager *icl.AccountManager) (map[icl.Environment]string, string, error) {
	path, err := icl.SessionPath()
	if err != nil {
		return nil, "", fmt.Errorf("resolve auth session: %w", err)
	}
	tokens, err := icl.LoadSession(path)
	if err != nil {
		return nil, "", fmt.Errorf("load auth session: %w", err)
	}
	manager.SetRefreshTokens(tokens)
	return tokens, path, nil
}

func saveRotatedQuerySession(path string, before, after map[icl.Environment]string) error {
	if maps.Equal(before, after) {
		return nil
	}
	if err := icl.SaveSession(path, after); err != nil {
		return fmt.Errorf("save rotated auth session: %w", err)
	}
	return nil
}

const (
	queryErrorMaxDistinct     = 8
	queryErrorMaxMessageBytes = 2048
)

type queryErrorIssue struct {
	message string
	count   uint64
}

type queryErrors struct {
	issues  []queryErrorIssue
	omitted uint64
}

func (e *queryErrors) add(err error) {
	message := boundedQueryErrorMessage(err.Error())
	for i := range e.issues {
		if e.issues[i].message == message {
			e.issues[i].count++
			return
		}
	}
	if len(e.issues) == queryErrorMaxDistinct {
		e.omitted++
		return
	}
	e.issues = append(e.issues, queryErrorIssue{message: message, count: 1})
}

func (e queryErrors) err() error {
	if len(e.issues) == 0 {
		return nil
	}
	var message strings.Builder
	for i, issue := range e.issues {
		if i > 0 {
			message.WriteByte('\n')
		}
		message.WriteString(issue.message)
		if issue.count > 1 {
			fmt.Fprintf(&message, " (repeated %d times)", issue.count)
		}
	}
	if e.omitted > 0 {
		fmt.Fprintf(&message, "\n%d additional error occurrences omitted", e.omitted)
	}
	return errors.New(message.String())
}

func boundedQueryErrorMessage(message string) string {
	if len(message) <= queryErrorMaxMessageBytes {
		return message
	}
	end := queryErrorMaxMessageBytes - len("...")
	for end > 0 && !utf8.RuneStart(message[end]) {
		end--
	}
	return message[:end] + "..."
}

type queryResult struct {
	mu       *sync.RWMutex
	logs     []icl.Log
	warnings []string
	errors   queryErrors
	failure  queryFailure
}

type queryResultSnapshot struct {
	logs     []icl.Log
	warnings []string
	err      error
	failure  queryFailure
}

func (r *queryResult) addData(item *icl.StreamItem, logs []icl.Log, warns, errs []string) (hasWarnings, hasErrors bool) {
	r.mutex().Lock()
	defer r.mutex().Unlock()
	r.logs = append(r.logs, logs...)
	r.warnings = append(r.warnings, warns...)
	if len(errs) == 0 {
		return len(warns) > 0, false
	}
	failure := queryFailureGeneral
	if item != nil && (item.Error != nil || len(item.Errors) > 0) {
		failure = queryFailureData
	}
	for _, message := range errs {
		r.failedLocked(errors.New(message), failure)
	}
	return len(warns) > 0, true
}

func (r *queryResult) failed(err error, failure queryFailure) {
	if err == nil {
		return
	}
	r.mutex().Lock()
	defer r.mutex().Unlock()
	r.failedLocked(err, failure)
}

func (r *queryResult) failedLocked(err error, failure queryFailure) {
	first := len(r.errors.issues) == 0
	r.errors.add(err)
	if first {
		r.failure = failure
		return
	}
	if r.failure != queryFailureData {
		r.failure = failure
	}
}

func (r *queryResult) snapshot() queryResultSnapshot {
	r.mutex().RLock()
	defer r.mutex().RUnlock()
	return queryResultSnapshot{logs: append([]icl.Log(nil), r.logs...), warnings: append([]string(nil), r.warnings...), err: r.errors.err(), failure: r.failure}
}

func (r *queryResult) mutex() *sync.RWMutex {
	if r.mu == nil {
		r.mu = &sync.RWMutex{}
	}
	return r.mu
}

func (m *queryMember) resultSnapshot() queryResultSnapshot { return m.result.snapshot() }

func (m *queryMember) OnData(item *icl.StreamItem) {
	logs, warns, errs := icl.DecodeStreamItem(item)
	if m.first != nil && !m.first.acceptData(m.index, len(logs)) {
		return
	}
	if m.tee != nil {
		for _, log := range logs {
			if err := m.tee.emit(log); err != nil {
				return
			}
		}
	}
	hasWarnings, hasErrors := m.result.addData(item, logs, warns, errs)
	switch {
	case hasErrors:
		m.setStreamPhase(status.Error)
	case hasWarnings:
		m.setStreamPhase(status.Warning)
	}
}

func (m *queryMember) OnClose() {
	if m.first != nil && !m.first.accepts(m.index) {
		return
	}
	result := m.resultSnapshot()
	phase := status.Success
	if result.err != nil {
		phase = status.Error
	} else if len(result.warnings) > 0 {
		phase = status.Warning
	}
	m.setPhase(phase, queryNow())
}

func (m *queryMember) OnKeepAlive() {}
func (m *queryMember) OnError(err error) {
	if m.first != nil && !m.first.accepts(m.index) {
		return
	}
	m.failed(err, classifyQueryError(err))
}

func (m *queryMember) failed(err error, failure queryFailure) {
	m.result.failed(err, failure)
	m.setPhase(status.Error, queryNow())
}

func classifyQueryError(err error) queryFailure {
	if icl.IsQueryDataRejection(err) {
		return queryFailureData
	}
	if icl.IsQueryServiceUnavailable(err) {
		return queryFailureUnavailable
	}
	return queryFailureGeneral
}

// readQueryStdin reads command input verbatim. The actual command input is an
// *os.File, whose platform implementation polls for readability so an open
// pipe observes cancellation without a blocked reader goroutine.
func readQueryStdin(ctx context.Context, in io.Reader) ([]byte, error) {
	queryInputReadStarted()
	if file, ok := in.(*os.File); ok {
		return readQueryFile(ctx, file)
	}
	data, err := io.ReadAll(in)
	if err != nil {
		return nil, fmt.Errorf("read query input: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

//nolint:gocyclo // snapshot lifecycle checkpoints preserve cancellation ordering.
func finalizeQuerySnapshot(ctx context.Context, stderr io.Writer, cfg *config.Config, members []queryMember, query string, startedAt time.Time) (string, bool, string, error) {
	if err := checkpointQuerySnapshot(ctx, "before snapshot directory"); err != nil {
		return "", false, "", err
	}
	dir, err := querySnapshotDir()
	if err != nil {
		return "", false, "", fmt.Errorf("create snapshot directory: %w", err)
	}
	if err := checkpointQuerySnapshot(ctx, "after snapshot directory"); err != nil {
		return "", false, "", err
	}
	finalPath, err := writeQuerySnapshot(ctx, dir, members, query, startedAt)
	var publishErr error
	if err != nil {
		if !publishedCleanupError(finalPath, err) {
			return "", false, finalPath, err
		}
		publishErr = err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(finalPath)
		}
	}()
	if err := checkpointQuerySnapshot(ctx, "before retention"); err != nil {
		return "", false, finalPath, err
	}
	if _, err := queryRetainSnapshots(cfg.Core.MaxAutoSnapshots); err != nil {
		slog.Warn("auto snapshot retention failed", "error", sanitizeStderrText(err.Error()))
		queryWriteStderrf(stderr, "warning: auto snapshot retention failed: %v", err)
	}
	if err := checkpointQuerySnapshot(ctx, "after retention"); err != nil {
		return "", false, finalPath, err
	}
	if err := checkpointQuerySnapshot(ctx, "before notification"); err != nil {
		return "", false, finalPath, err
	}
	notifySnapshotsDirtyContext(ctx, func(format string, a ...any) { queryWriteStderrf(stderr, strings.TrimSuffix(format, "\n"), a...) })
	if err := checkpointQuerySnapshot(ctx, "after notification"); err != nil {
		return "", false, finalPath, err
	}
	selector, retained, err := resolveRetainedQuerySnapshot(finalPath)
	if err != nil {
		return "", false, finalPath, err
	}
	if err := checkpointQuerySnapshot(ctx, "before snapshot outcome"); err != nil {
		return "", false, finalPath, err
	}
	keep = true
	return selector, retained, finalPath, publishErr
}

// publishedCleanupError reports whether err is the typed source-cleanup
// outcome of a successful automatic publication.
func publishedCleanupError(finalPath string, err error) bool {
	var cleanupErr *snapshot.RenameCleanupError
	return finalPath != "" && errors.As(err, &cleanupErr)
}

// writeQuerySnapshot is the sole frame writer. Workers collect in memory and
// this function writes every selected member in resolved selector order, which keeps
// container frame appends serialized and makes zero/error frames explicit.
//
//nolint:gocyclo // each checkpoint protects a durable snapshot lifecycle phase.
func writeQuerySnapshot(ctx context.Context, dir string, members []queryMember, query string, startedAt time.Time) (finalPath string, err error) {
	file, err := snapshot.CreateWip(dir, os.Getpid())
	if err != nil {
		return "", fmt.Errorf("create snapshot: %w", err)
	}
	wipPath := file.Name()
	published := false
	defer func() {
		_ = file.Close()
		if err != nil && !published {
			_ = os.Remove(wipPath)
		}
	}()
	if err := checkpointQuerySnapshot(ctx, "after snapshot create"); err != nil {
		return "", err
	}
	container := snapshot.NewWriter(file)
	logsSizeBytes := make([]uint64, len(members))
	for i := range members {
		member := &members[i]
		var err error
		logsSizeBytes[i], err = snapshot.SaveInstanceFrameWithSize(container, member.instance.CRN.String(), member.resultSnapshot().logs)
		if err != nil {
			return "", fmt.Errorf("write snapshot logs for %s: %w", member.instance.Name, err)
		}
	}
	if err := checkpointQuerySnapshot(ctx, "after snapshot logs"); err != nil {
		return "", err
	}
	savedAt := queryNow()
	if err := snapshot.SaveStateFrame(container, querySnapshotMeta(savedAt, members, logsSizeBytes, query)); err != nil {
		return "", fmt.Errorf("write snapshot state: %w", err)
	}
	if err := checkpointQuerySnapshot(ctx, "after snapshot state"); err != nil {
		return "", err
	}
	if err := closeQuerySnapshot(file, container); err != nil {
		return "", err
	}
	if err := checkpointQuerySnapshot(ctx, "after snapshot close"); err != nil {
		return "", err
	}
	if err := checkpointQuerySnapshot(ctx, "before snapshot rename"); err != nil {
		return "", err
	}
	finalPath, err = queryPublishAutoSnapshot(wipPath, startedAt, nil)
	if finalPath != "" {
		published = true
	}
	if err != nil {
		return finalPath, fmt.Errorf("finalize snapshot: %w", err)
	}
	if err := checkpointQuerySnapshot(ctx, "after snapshot rename"); err != nil {
		return finalPath, err
	}
	return finalPath, nil
}

// checkpointQuerySnapshot brackets each blocking snapshot phase. Its hook is a
// deterministic test barrier; production uses the no-op default.
func checkpointQuerySnapshot(ctx context.Context, phase string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	querySnapshotCheckpoint(phase)
	return ctx.Err()
}

func querySnapshotMeta(now time.Time, members []queryMember, logsSizeBytes []uint64, query string) snapshot.Snapshot {
	micros := now.UnixMicro()
	instances := make([]snapshot.InstanceSnapshot, 0, len(members))
	for i := range members {
		member := &members[i]
		phase, message := member.stateAndMessage()
		view := member.view()
		instances = append(instances, snapshot.InstanceSnapshot{
			CRN: member.instance.CRN.String(), State: int(phase), LogCount: len(member.resultSnapshot().logs), LogsSizeBytes: logsSizeBytes[i], Message: message,
			StartTimeMicro: view.Started.UnixMicro(), LastUpdateTimeMicro: micros,
		})
	}
	return snapshot.Snapshot{Query: query, InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: instances}}
}

func closeQuerySnapshot(file *os.File, container *snapshot.Container) error {
	if err := container.Close(); err != nil {
		return fmt.Errorf("close snapshot container: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync snapshot: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close snapshot file: %w", err)
	}
	return nil
}

func resolveRetainedQuerySnapshot(finalPath string) (string, bool, error) {
	if _, err := os.Stat(finalPath); errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	} else if err != nil {
		return "", false, fmt.Errorf("check retained snapshot: %w", err)
	}
	name := strings.TrimSuffix(filepath.Base(finalPath), snapshot.FileExt)
	entry, err := snapshot.Resolve(name)
	if err != nil {
		return "", false, fmt.Errorf("resolve finalized snapshot: %w", err)
	}
	if entry.Path != finalPath {
		return "", false, errors.New("resolved snapshot path differs from finalized path")
	}
	return name, true, nil
}

var _ icl.QueryCallback = (*queryMember)(nil)
