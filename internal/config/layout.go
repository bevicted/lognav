package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// newConfig inits a Config object with default values.
//
//nolint:funlen // single literal initializing all default config fields; splitting harms readability.
func newConfig() *Config {
	// include configs even if the default empty value is used
	return &Config{
		Keys: Keys{
			Accept:    KeyBind{"enter", "ctrl+y"},
			All:       KeyBind{"a"},
			Cancel:    KeyBind{"esc"},
			Clear:     KeyBind{"ctrl+c"},
			Delete:    KeyBind{"del", "shift+x"},
			ForceQuit: KeyBind{"ctrl+c"},
			Help:      KeyBind{"?"},
			Next:      KeyBind{"tab", "ctrl+n", "down", "right"},
			No:        KeyBind{"n"},
			Prev:      KeyBind{"shift+tab", "ctrl+p", "up", "left"},
			Quit:      KeyBind{"q"},
			Search:    KeyBind{"/"},
			Select:    KeyBind{"space"},
			Yes:       KeyBind{"y"},

			MoveToTop:        KeyBind{"g"},
			MovePageUp:       KeyBind{"pgup"},
			MoveHalfPageUp:   KeyBind{"u", "ctrl+u"},
			MoveUp:           KeyBind{"up", "k"},
			MoveToLastChar:   KeyBind{"$"},
			MoveRightN:       KeyBind{"shift+right", "shift+l"},
			MoveRight:        KeyBind{"right", "l"},
			Center:           KeyBind{"z"},
			MoveLeft:         KeyBind{"left", "h"},
			MoveLeftN:        KeyBind{"shift+left", "shift+h"},
			MoveDown:         KeyBind{"down", "j"},
			MoveHalfPageDown: KeyBind{"d", "ctrl+d"},
			MovePageDown:     KeyBind{"pgdown"},
			MoveToBottom:     KeyBind{"shift+g"},
			MoveToFirstChar:  KeyBind{"^"},
			MoveToFirstCol:   KeyBind{"0"},

			CenterPrevSearchMatch: KeyBind{"shift+n"},
			CenterNextSearchMatch: KeyBind{"n"},

			CancelAllFetches: KeyBind{"shift+c"},
			CopyEntire:       KeyBind{"shift+y"},
			CopyValue:        KeyBind{"y"},
			Editor:           KeyBind{"e"},
			Export:           KeyBind{"e"},
			ArchiveDispatch:  KeyBind{"shift+f"},
			FetchLogs:        KeyBind{"f"},
			FirstFetch:       KeyBind{"shift+f"},
			FilesMenu:        KeyBind{"f"},
			FilterMenu:       KeyBind{"f"},
			Jq:               KeyBind{"\\"},
			PrevTab:          KeyBind{"ctrl+^"},
			ContextMenu:      KeyBind{"c"},
			JqField:          KeyBind{"ctrl+\\"},
			SearchValue:      KeyBind{"ctrl+/", "ctrl+_"},
			Rename:           KeyBind{"r"},
			RetryFailedFetch: KeyBind{"r"},
			Watch:            KeyBind{"w"},
			Snippets:         KeyBind{"tab"},
			Snapshot:         KeyBind{"ctrl+s"},
			TabLeft:          KeyBind{"<"},
			TabRight:         KeyBind{">"},
			Timeline:         KeyBind{"t"},
			ToggleLogMark:    KeyBind{"m"},
		},
		Style: Style{
			Bg:           ConfigColor{},
			Fg:           ConfigColor{},
			KeyHintFg:    ConfigColor{ansi.Yellow},
			TablineBg:    ConfigColor{},
			TablineFg:    ConfigColor{ansi.White},
			TablineAlign: 0.5,

			CancelledLabel:         "CANCEL",
			DisabledLabel:          "OFF",
			EnabledLabel:           "READY",
			ErrorLabel:             "ERROR",
			InProgressLabel:        "FETCHING",
			WatchingLabel:          "WATCH",
			SuccessLabel:           "DONE",
			WarningLabel:           "WARN",
			AuthInProgressLabel:    "AUTH",
			ReadOnlyLabel:          "READ ONLY",
			ElapsedFetchTimeFormat: "%.2fs",

			GutterFg:            ConfigColor{ansi.Black},
			AuthInProgressColor: ConfigColor{ansi.Magenta},
			CancelledColor:      ConfigColor{ansi.BrightBlack},
			DisabledColor:       ConfigColor{ansi.BrightBlack},
			ReadOnlyColor:       ConfigColor{ansi.BrightBlack},
			CriticalColor:       ConfigColor{ansi.BrightMagenta},
			DebugColor:          ConfigColor{ansi.Cyan},
			EnabledColor:        ConfigColor{ansi.White},
			ErrorColor:          ConfigColor{ansi.Red},
			InfoColor:           ConfigColor{ansi.Blue},
			InProgressColor:     ConfigColor{ansi.Cyan},
			WatchingColor:       ConfigColor{ansi.Cyan},
			SuccessColor:        ConfigColor{ansi.Green},
			VerboseColor:        ConfigColor{ansi.BrightBlack},
			WarningColor:        ConfigColor{ansi.Yellow},
			UnknownColor:        ConfigColor{ansi.BrightBlack},

			EverySecondListItemBg: ConfigColor{},
			HoverRowBg:            ConfigColor{ansi.BrightBlack},
			SearchMatchBg:         ConfigColor{ansi.Black},
			SearchMatchFg:         ConfigColor{ansi.Yellow},
			MarkBg:                ConfigColor{ansi.Green},
			MarkFg:                ConfigColor{ansi.Black},
			UrlFg:                 ConfigColor{ansi.Blue},
			PillChipBg:            ConfigColor{ansi.Blue},
			PillLineBg:            ConfigColor{ansi.White},
			PillFg:                ConfigColor{ansi.Black},

			JsonKeyFg:      ConfigColor{ansi.Blue},
			JsonStringFg:   ConfigColor{ansi.Green},
			JsonNumberFg:   ConfigColor{ansi.Yellow},
			JsonBoolNullFg: ConfigColor{ansi.Magenta},

			DpKeywordFg:  ConfigColor{ansi.Yellow},
			DpOperatorFg: ConfigColor{},
			DpStringFg:   ConfigColor{ansi.Green},
			DpNumberFg:   ConfigColor{ansi.Yellow},
			DpCommentFg:  ConfigColor{ansi.BrightBlack},
			DpFunctionFg: ConfigColor{ansi.Blue},
			DpVariableFg: ConfigColor{ansi.Magenta},
			DpTypeFg:     ConfigColor{ansi.Cyan},
			DpBoolNullFg: ConfigColor{ansi.Magenta},
			DpErrorFg:    ConfigColor{ansi.Red},
		},
		ICL: ICL{
			Instances: []ICLInstanceConfig{},
			Environments: map[string]ICLEnvironmentConfig{
				"bluemix": {IAMURL: "https://iam.cloud.ibm.com/identity"},
			},
			DefaultQuery: `source logs between @'{{ date -1 }}' and @'now'
| filter $l.subsystemname == 'example-service'
// | filter $d ~~ 'search for something'
| orderby $m.timestamp asc
`,
		},
		Logs: Logs{
			JqDefaultQuery:           ".data",
			JqTimeoutMs:              5000,
			BatchOpChunkSize:         2000,
			RenderCacheSize:          128,
			HorizontalMoveNAmount:    10,
			Scrolloff:                7,
			TimelineBuckets:          100,
			TimelineHideEmptyBuckets: true,
			MaxRows:                  0,
			ResidentInstances:        1,
			DefaultColorRules: ColorRuleList{
				{Match: "error", Fg: ConfigColor{ansi.Red}},
			},
			IncludeDefaultColorRules: true,
		},
		Core: Core{
			ShowKeyHints: true,
			DefaultSnippets: []Snippet{
				// top-level fields
				{"$m", "metadata (e.g. $m.timestamp)"},
				{"$l", "labels (e.g. $l.subsystemname)"},
				{"$d", "data (default, so $d.log == log)"},

				// timeframes
				{"last 1h", "timeframe: last duration"},
				{"between @'{{ date -1 }}' and @'{{ date }}'", "timeframe: between two timestamps"},
				{"around @'{{ date }}' interval 30m", "timeframe: around timestamp with interval"},
				{"@'{{ timestamp }}'", "timestamp format (ISO 8601)"},

				// filtering
				{"| filter $l.subsystemname == 'service'", "keep rows matching condition (alias: f)"},
				{"| f log.status_code >= 500", "filter: numeric comparison"},
				{"| f log.msg ~ 'substring'", "filter: substring match in field"},
				{"| f $d ~~ 'substring'", "filter: substring match in top-level"},
				{"| f log.field != null", "filter: field exists"},
				{"| f log.field.in('a', 'b', 'c')", "filter: membership test"},
				{"| block log.msg ~ 'healthcheck'", "remove rows matching condition (inverse of filter)"},

				// text search
				{"| find 'text' in $d", "full-text search in top-level field"},
				{"| find 'text' in log.msg", "full-text search in specific field"},
				{"| wildfind 'text'", "search across all top-level fields"},
				{"| lucene 'status:500 AND method:GET'", "search using Lucene query syntax"},

				// selection
				{"| choose $m.timestamp, log.msg, log.status_code", "select specific fields (like SQL SELECT)"},
				{"| choose log.field as alias", "select with rename"},

				// aggregation (incompatible with orderby)
				{"| groupby log.field", "group results by field"},
				{"| groupby log.field aggregate count() as cnt", "group by with aggregate function"},
				{"| groupby log.field aggregate distinct_count(log.other) as alias", "group by with distinct count"},
				{"| countby log.field", "shorthand for groupby ... aggregate count()"},
				{"| count", "count all matching logs"},
				{"| distinct log.field", "deduplicate results by field"},
				{"| aggregate count() as total, avg(log.duration) as avg_dur", "aggregate without grouping"},

				// sorting and limiting (incompatible with aggregation)
				{"| orderby $m.timestamp asc", "sort results (asc/desc)"},
				{"| top 10 log.field by log.duration", "return highest N values"},
				{"| bottom 5 log.field by log.duration", "return lowest N values"},
				{"| limit 500", "restrict result count"},

				// data manipulation
				{"| create log.duration * 1000 as duration_ms", "generate new field from expression (aliases: add, c)"},
				{"| replace log.msg with toLowerCase(log.msg)", "modify field value in-place"},
				{"| remove log.field", "delete field from output"},
				{"| move log.old to log.new", "rename or relocate a field"},
				{"| convert log.field to number", "change field data type"},
				{"| extract log.msg into a, b using /(?P<a>\\w+) (?P<b>\\S+)/", "parse string into fields using regex"},
				{"| redact log.msg matching /pattern/ to '[REDACTED]'", "mask sensitive data"},

				// advanced
				{"| explode log.tags", "expand array field into one row per element"},

				// common functions
				{"count()", "count rows"},
				{"count_if(condition)", "count rows matching condition"},
				{"distinct_count(field)", "count unique values"},
				{"avg(field)", "average of values"},
				{"sum(field)", "sum of values"},
				{"min(field)", "minimum value"},
				{"max(field)", "maximum value"},
				{"percentile(field, 95)", "p-th percentile"},
				{"contains(field, 'str')", "true if field contains str"},
				{"concat(a, ' ', b)", "concatenate strings"},
				{"toLowerCase(field)", "convert to lowercase"},
				{"now()", "current timestamp"},
				{"if(cond, then, else)", "ternary conditional"},
				{"firstNonNull(a, b)", "return first non-null argument"},
			},
			ExtraSnippets:           nil,
			IncludeDefaultSnippets:  true,
			MaxAutoSnapshots:        10,
			MaxLogFiles:             10,
			EnableMouse:             true,
			EnableHover:             true,
			DoubleClickMs:           400,
			ScrollAxisLockMs:        150,
			WheelScrollLines:        5,
			RedrawIntervalMs:        33,
			SaveSnapshotOnFetchDone: true,
			NotifyOnFetchDone:       true,
			NotifyStyle:             NotifyStyleOSC9,
			IncludeSnippetsInEditor: true,
			WatchCooldownSeconds:    15,
			WatchMaxFetches:         0,
			WatchMaxDurationSeconds: 1800,
		},
		Version: CurrentVersion,
	}
}

type Snippet struct {
	Snippet string `yaml:"snippet" desc:"snippet text to insert"`
	Desc    string `yaml:"desc"    desc:"description shown next to the snippet"`
}

type Config struct {
	Keys    Keys  `yaml:"keys"    desc:"keybinding configuration"`
	Style   Style `yaml:"style"  desc:"visual styling and color configuration"`
	ICL     ICL   `yaml:"icl"     desc:"IBM Cloud Logs instance configuration"`
	Logs    Logs  `yaml:"logs"     desc:"log viewer behavior settings"`
	Core    Core  `yaml:"core"     desc:"general application behavior"`
	Version int   `yaml:"version" desc:"config file version"`
}

type KeyBind []string

type Keys struct {
	// generic

	Accept    KeyBind `yaml:"accept"    desc:"confirm selection or action"`
	All       KeyBind `yaml:"all"       desc:"select all items"`
	Cancel    KeyBind `yaml:"cancel"    desc:"cancel current operation"`
	Clear     KeyBind `yaml:"clear"     desc:"clear current input"`
	Delete    KeyBind `yaml:"delete"    desc:"delete selected item"`
	ForceQuit KeyBind `yaml:"-"         desc:"exit the application: ctrl+c pressed twice in quick succession (not configurable)"`
	Help      KeyBind `yaml:"help"      desc:"show help overlay"`
	Next      KeyBind `yaml:"next"      desc:"move to next item"`
	No        KeyBind `yaml:"no"        desc:"decline prompt"`
	Prev      KeyBind `yaml:"prev"      desc:"move to previous item"`
	Quit      KeyBind `yaml:"quit"      desc:"quit the application"`
	Search    KeyBind `yaml:"search"    desc:"focus search bar"`
	Select    KeyBind `yaml:"select"    desc:"toggle item selection"`
	Yes       KeyBind `yaml:"yes"       desc:"confirm prompt"`

	// movement

	MoveToTop        KeyBind `yaml:"moveToTop"        desc:"scroll to top"`
	MovePageUp       KeyBind `yaml:"movePageUp"       desc:"scroll up one page"`
	MoveHalfPageUp   KeyBind `yaml:"moveHalfPageUp"   desc:"scroll up half a page"`
	MoveUp           KeyBind `yaml:"moveUp"           desc:"move cursor up one line"`
	MoveToLastChar   KeyBind `yaml:"moveToLastChar"   desc:"move to last character on line"`
	MoveRightN       KeyBind `yaml:"moveRightN"       desc:"move right by horizontalMoveNAmount columns"`
	MoveRight        KeyBind `yaml:"moveRight"        desc:"move right one column"`
	Center           KeyBind `yaml:"center"           desc:"center viewport on cursor"`
	MoveLeft         KeyBind `yaml:"moveLeft"         desc:"move left one column"`
	MoveLeftN        KeyBind `yaml:"moveLeftN"        desc:"move left by horizontalMoveNAmount columns"`
	MoveDown         KeyBind `yaml:"moveDown"         desc:"move cursor down one line"`
	MoveHalfPageDown KeyBind `yaml:"moveHalfPageDown" desc:"scroll down half a page"`
	MovePageDown     KeyBind `yaml:"movePageDown"     desc:"scroll down one page"`
	MoveToBottom     KeyBind `yaml:"moveToBottom"     desc:"scroll to bottom"`
	MoveToFirstChar  KeyBind `yaml:"moveToFirstChar"  desc:"move to first non-space character on line"`
	MoveToFirstCol   KeyBind `yaml:"moveToFirstCol"   desc:"move to first column"`

	CenterPrevSearchMatch KeyBind `yaml:"centerPrevSearchMatch" desc:"center on previous search match"`
	CenterNextSearchMatch KeyBind `yaml:"centerNextSearchMatch" desc:"center on next search match"`

	// specific
	// last resort, prefer generic section

	ArchiveDispatch  KeyBind `yaml:"archiveDispatch"  desc:"dispatch enabled instances' query as background (archive) queries"`
	CancelAllFetches KeyBind `yaml:"cancelAllFetches" desc:"cancel all ongoing log fetches"`
	CopyEntire       KeyBind `yaml:"copyEntire"       desc:"copy entire data under cursor"`
	CopyValue        KeyBind `yaml:"copyValue"        desc:"copy value under cursor, fallback to entire"`
	Editor           KeyBind `yaml:"editor"           desc:"open query in external editor"`
	Export           KeyBind `yaml:"export"           desc:"export logs to file"`
	FetchLogs        KeyBind `yaml:"fetchLogs"        desc:"fetch logs for enabled instances"`
	FirstFetch       KeyBind `yaml:"firstFetch"       desc:"race enabled instances until the first logs arrive"`
	FilesMenu        KeyBind `yaml:"filesMenu"        desc:"open file browser menu"`
	FilterMenu       KeyBind `yaml:"filterMenu"       desc:"open filter menu in logviewer"`
	Jq               KeyBind `yaml:"jq"               desc:"open jq filter bar"`
	PrevTab          KeyBind `yaml:"prevTab"          desc:"switch to previous tab"`
	ContextMenu      KeyBind `yaml:"contextMenu"      desc:"open context menu for the current selection"`
	JqField          KeyBind `yaml:"jqField"          desc:"jq the field under the cursor (logviewer)"`
	SearchValue      KeyBind `yaml:"searchValue"      desc:"search the value under the cursor (logviewer)"`
	Rename           KeyBind `yaml:"rename"           desc:"rename selected item"`
	RetryFailedFetch KeyBind `yaml:"retryFailedFetch" desc:"retry failed log fetches"`
	Watch            KeyBind `yaml:"watch"            desc:"watch enabled instances, retry until logs found"`
	Snippets         KeyBind `yaml:"snippets"         desc:"open snippets menu in query editor"`
	Snapshot         KeyBind `yaml:"snapshot"         desc:"save or restore a snapshot"`
	TabLeft          KeyBind `yaml:"tabLeft"          desc:"move tab left in tab bar"`
	TabRight         KeyBind `yaml:"tabRight"         desc:"move tab right in tab bar"`
	Timeline         KeyBind `yaml:"timeline"         desc:"toggle timeline view in logviewer"`
	ToggleLogMark    KeyBind `yaml:"toggleLogMark"    desc:"toggle mark on current log line"`
}

type Style struct {
	Bg           Color   `yaml:"bg"                        desc:"main background color"`
	Fg           Color   `yaml:"fg"                        desc:"main foreground color"`
	KeyHintFg    Color   `yaml:"keyHintFg"                 desc:"key hint text color"`
	TablineBg    Color   `yaml:"tablineBg"                 desc:"tab bar background color"`
	TablineFg    Color   `yaml:"tablineFg"                 desc:"tab bar foreground color"`
	TablineAlign float32 `yaml:"tablineAlign"              desc:"tab bar alignment, 0.0 (left) to 1.0 (right)"`

	CancelledLabel         string `yaml:"cancelledLabel"         desc:"label shown for cancelled state"`
	DisabledLabel          string `yaml:"disabledLabel"          desc:"label shown for disabled state"`
	EnabledLabel           string `yaml:"enabledLabel"           desc:"label shown for enabled state"`
	ErrorLabel             string `yaml:"errorLabel"             desc:"label shown for error state"`
	InProgressLabel        string `yaml:"inProgressLabel"        desc:"label shown for in-progress (fetching) state"`
	WatchingLabel          string `yaml:"watchingLabel"          desc:"label shown for watching state"`
	SuccessLabel           string `yaml:"successLabel"           desc:"label shown for success state"`
	WarningLabel           string `yaml:"warningLabel"           desc:"label shown for warning state"`
	AuthInProgressLabel    string `yaml:"authInProgressLabel"    desc:"label shown for the per-instance auth-in-progress state"`
	ReadOnlyLabel          string `yaml:"readOnlyLabel"          desc:"label shown for snapshot-only read-only state"`
	ElapsedFetchTimeFormat string `yaml:"elapsedFetchTimeFormat" desc:"Go format string for elapsed fetch time display"`

	GutterFg            Color `yaml:"gutterFg"            desc:"shared foreground color for severity gutter"`
	AuthInProgressColor Color `yaml:"authInProgressColor" desc:"color for the per-instance auth-in-progress state"`
	CancelledColor      Color `yaml:"cancelledColor"      desc:"color for cancelled state label"`
	DisabledColor       Color `yaml:"disabledColor"       desc:"color for disabled state label"`
	ReadOnlyColor       Color `yaml:"readOnlyColor"       desc:"color for snapshot-only read-only state label"`
	CriticalColor       Color `yaml:"criticalColor"       desc:"color for critical severity"`
	DebugColor          Color `yaml:"debugColor"      desc:"color for debug severity"`
	EnabledColor        Color `yaml:"enabledColor"    desc:"color for enabled state"`
	ErrorColor          Color `yaml:"errorColor"      desc:"color for error state and severity"`
	InfoColor           Color `yaml:"infoColor"       desc:"color for info severity"`
	InProgressColor     Color `yaml:"inProgressColor" desc:"color for in-progress state"`
	WatchingColor       Color `yaml:"watchingColor"   desc:"color for watching state"`
	SuccessColor        Color `yaml:"successColor"    desc:"color for success state"`
	VerboseColor        Color `yaml:"verboseColor"    desc:"color for verbose severity"`
	WarningColor        Color `yaml:"warningColor"    desc:"color for warning state and severity"`
	UnknownColor        Color `yaml:"unknownColor"    desc:"color for unknown severity"`

	EverySecondListItemBg Color `yaml:"everySecondListItemBg" desc:"alternating row background color"`
	HoverRowBg            Color `yaml:"hoverRowBg"            desc:"experimental: background tint for the log row under the mouse pointer (requires core.enableHover; unset disables the tint)"`
	SearchMatchBg         Color `yaml:"searchMatchBg"         desc:"search highlight background color"`
	SearchMatchFg         Color `yaml:"searchMatchFg"         desc:"search highlight foreground color"`
	MarkBg                Color `yaml:"markBg"                desc:"marked line background color"`
	MarkFg                Color `yaml:"markFg"                desc:"marked line foreground color"`
	UrlFg                 Color `yaml:"urlFg"                 desc:"foreground color for URLs/hyperlinks"`
	PillChipBg            Color `yaml:"pillChipBg"            desc:"chip/label background for pills (jq/search/filter)"`
	PillLineBg            Color `yaml:"pillLineBg"            desc:"value band background for pills"`
	PillFg                Color `yaml:"pillFg"                desc:"text (foreground) color for the whole pill (chip + value band)"`

	JsonKeyFg      Color `yaml:"jsonKeyFg"      desc:"foreground color for JSON object keys"`
	JsonStringFg   Color `yaml:"jsonStringFg"   desc:"foreground color for JSON string values"`
	JsonNumberFg   Color `yaml:"jsonNumberFg"    desc:"foreground color for JSON number values"`
	JsonBoolNullFg Color `yaml:"jsonBoolNullFg"  desc:"foreground color for JSON booleans and null"`

	DpKeywordFg  Color `yaml:"dpKeywordFg"  desc:"foreground color for dataprime keywords"`
	DpOperatorFg Color `yaml:"dpOperatorFg" desc:"foreground color for dataprime operators"`
	DpStringFg   Color `yaml:"dpStringFg"   desc:"foreground color for dataprime strings"`
	DpNumberFg   Color `yaml:"dpNumberFg"   desc:"foreground color for dataprime numbers and intervals"`
	DpCommentFg  Color `yaml:"dpCommentFg"  desc:"foreground color for dataprime comments"`
	DpFunctionFg Color `yaml:"dpFunctionFg" desc:"foreground color for dataprime function calls"`
	DpVariableFg Color `yaml:"dpVariableFg" desc:"foreground color for dataprime variables and fields"`
	DpTypeFg     Color `yaml:"dpTypeFg"     desc:"foreground color for dataprime type names"`
	DpBoolNullFg Color `yaml:"dpBoolNullFg" desc:"foreground color for dataprime booleans and null"`
	DpErrorFg    Color `yaml:"dpErrorFg"    desc:"foreground color for dataprime syntax errors"`
}

type ICLInstanceConfig struct {
	Name string `yaml:"name" desc:"instance display name"`
	CRN  *CRN   `yaml:"crn"  desc:"Cloud Resource Name for the ICL instance"`
}

// EffectiveInstance is the display-only representation of an effective ICL
// instance. It is computed from built-in and extra instances and is not a
// config-file field.
type EffectiveInstance struct {
	Name        string `yaml:"name" json:"name"`
	Region      string `yaml:"region" json:"region"`
	Environment string `yaml:"environment" json:"environment"`
	CRN         string `yaml:"crn" json:"crn"`
}

type ICL struct {
	Instances    []ICLInstanceConfig             `yaml:"instances"    desc:"configured ICL instances"`
	Environments map[string]ICLEnvironmentConfig `yaml:"environments" desc:"IAM environments keyed by CRN CName"`
	DefaultQuery string                          `yaml:"defaultQuery" desc:"Dataprime query template resolved on startup"`
}

// ICLEnvironmentConfig supplies one IAM discovery endpoint and its optional
// noninteractive credentials. APIKeyOpRef is a 1Password reference, not a secret.
type ICLEnvironmentConfig struct {
	IAMURL      string `yaml:"iamURL"      desc:"IAM OIDC discovery URL"`
	APIKey      string `yaml:"apiKey"      desc:"IBM Cloud API key"`
	APIKeyOpRef string `yaml:"apiKeyOpRef" desc:"1Password secret reference for IBM Cloud API key"`
}

// EffectiveInstances returns a distinct, non-nil copy of the configured
// instances so callers cannot mutate config through its backing storage.
func EffectiveInstances(cfg *Config) []ICLInstanceConfig {
	return append([]ICLInstanceConfig{}, cfg.ICL.Instances...)
}

// EffectiveInstanceRows returns effective ICL instances in their display-only
// form. Region and environment are always derived from the CRN.
func EffectiveInstanceRows(cfg *Config) []EffectiveInstance {
	instances := EffectiveInstances(cfg)
	rows := make([]EffectiveInstance, 0, len(instances))
	for _, instance := range instances {
		row := EffectiveInstance{Name: instance.Name}
		if instance.CRN != nil {
			row.Region = instance.CRN.Location
			row.Environment = instance.CRN.CName
			row.CRN = instance.CRN.String()
		}
		rows = append(rows, row)
	}
	return rows
}

// DisplayNameForCRN returns a configured display name for crn, or a compact
// CRN-derived label when crn is not configured. Compact labels are cosmetic and
// need not be unique.
func DisplayNameForCRN(instances []ICLInstanceConfig, crn *CRN) string {
	if crn == nil {
		return ""
	}
	for _, instance := range instances {
		if instance.CRN != nil && instance.CRN.String() == crn.String() {
			return instance.Name
		}
	}
	id := crn.InstanceID
	if len(id) > 8 {
		id = id[:8]
	}
	return crn.Location + "/" + id
}

// ValidateIAMURL rejects malformed IAM OIDC discovery URLs before they can be
// stored in configuration or used for a request.
func ValidateIAMURL(endpoint string) (string, error) {
	parsed, err := url.ParseRequestURI(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", errors.New("invalid IAM endpoint")
	}
	return parsed.String(), nil
}

// Validate confirms that every effective instance has the data its shared
// consumers require and that the set is unambiguous.
func (c *Config) Validate() error {
	if c.ICL.Instances == nil {
		return errors.New("icl.instances must be a list; use [] for no instances")
	}
	if c.ICL.Environments == nil {
		return errors.New("icl.environments must be a map")
	}
	for cname, environment := range c.ICL.Environments {
		if strings.TrimSpace(cname) == "" {
			return errors.New("icl.environments contains an empty CName")
		}
		if _, err := ValidateIAMURL(environment.IAMURL); err != nil {
			return fmt.Errorf("icl.environments.%q.iamURL: %w", cname, err)
		}
	}

	instances := EffectiveInstances(c)
	seenNames := make(map[string]struct{}, len(instances))
	seenCRNs := make(map[string]string, len(instances))
	for _, instance := range instances {
		if instance.Name == "" {
			return errors.New("effective instance is missing a name")
		}
		if strings.Contains(instance.Name, "/") {
			return fmt.Errorf("effective instance name %q must not contain /", instance.Name)
		}
		if instance.CRN == nil {
			return fmt.Errorf("effective instance %q is missing a CRN", instance.Name)
		}
		if _, ok := c.ICL.Environments[instance.CRN.CName]; !ok {
			return fmt.Errorf("effective instance %q uses unconfigured ICL environment %q", instance.Name, instance.CRN.CName)
		}
		if _, ok := seenNames[instance.Name]; ok {
			return fmt.Errorf("duplicate effective instance name %q", instance.Name)
		}
		seenNames[instance.Name] = struct{}{}
		crn := instance.CRN.String()
		if previousName, ok := seenCRNs[crn]; ok {
			return fmt.Errorf("duplicate effective instance CRN for %q and %q", previousName, instance.Name)
		}
		seenCRNs[crn] = instance.Name
	}
	return nil
}

type Logs struct {
	JqDefaultQuery           string `yaml:"jqDefaultQuery"  desc:"default jq expression applied to logs"`
	JqTimeoutMs              uint32 `yaml:"jqTimeoutMs"     desc:"jq evaluation timeout in milliseconds"`
	BatchOpChunkSize         uint16 `yaml:"batchOpChunkSize"          desc:"chunk size for batch operations"`
	RenderCacheSize          int    `yaml:"renderCacheSize"           desc:"LRU cache entries for rendered log lines. Higher trades memory for hit rate on long scrolls. Default 128."`
	HorizontalMoveNAmount    uint8  `yaml:"horizontalMoveNAmount"     desc:"columns to scroll with moveLeftN/moveRightN"`
	Scrolloff                uint8  `yaml:"scrolloff"                 desc:"lines to keep visible above/below cursor"`
	TimelineBuckets          uint   `yaml:"timelineBuckets"           desc:"number of time buckets shown in the timeline view"`
	TimelineHideEmptyBuckets bool   `yaml:"timelineHideEmptyBuckets"  desc:"skip empty buckets when rendering the timeline so quiet time slices collapse"`
	MaxRows                  uint32 `yaml:"maxRows"                   desc:"synchronous request and per-instance retention cap; 0 requests the default 50000 rows"`
	ResidentInstances        uint16 `yaml:"residentInstances"         desc:"instances kept fully resident in RAM; others are evicted to their on-disk frame and lazy-reloaded. 0 is treated as 1. Default 1."`

	DefaultColorRules        ColorRuleList `yaml:"-"                        desc:"built-in default color rules (not configurable)"`
	ExtraColorRules          ColorRuleList `yaml:"extraColorRules"          desc:"additional color rules; entries with the same match as a default rule override that default in place; an entry with no fg or bg disables a rule"`
	IncludeDefaultColorRules bool          `yaml:"includeDefaultColorRules" desc:"whether to include built-in default color rules"`
}

// EffectiveResidentInstances returns ResidentInstances, treating 0 as 1 so a
// hand-edited config can't brick the resident set.
func (l Logs) EffectiveResidentInstances() int {
	if l.ResidentInstances == 0 {
		return 1
	}
	return int(l.ResidentInstances)
}

type Core struct {
	ShowKeyHints            bool        `yaml:"showKeyHints"            desc:"show the clickable key-hint row below an optional contextual row; false returns the key-hint row to tab content"`
	DefaultSnippets         []Snippet   `yaml:"-"                       desc:"built-in default snippets (not configurable)"`
	ExtraSnippets           []Snippet   `yaml:"extraSnippets"           desc:"additional snippet templates shown before defaults in the snippet menu"`
	IncludeDefaultSnippets  bool        `yaml:"includeDefaultSnippets"  desc:"whether to include built-in default snippets"`
	MaxAutoSnapshots        uint8       `yaml:"maxAutoSnapshots"        desc:"max number of auto-saved snapshots"`
	MaxLogFiles             uint8       `yaml:"maxLogFiles"             desc:"max number of log files to retain in the state directory"`
	EnableMouse             bool        `yaml:"enableMouse"             desc:"enable mouse support"`
	EnableHover             bool        `yaml:"enableHover"             desc:"route mouse-motion events to highlight what is under the pointer: tint the log/list row and timeline bucket (style.hoverRowBg), move the dialog-button selection, and highlight the hovered tab on the tab bar (requires enableMouse; on by default)"`
	DoubleClickMs           uint16      `yaml:"doubleClickMs"           desc:"double-press detection window in milliseconds; two left-clicks on the same cell within this window activate the row (expand log / open instance / load snapshot), and two ctrl+c presses exit lognav. 0 disables double-click (mouse selects only; keyboard still activates) but ctrl+c keeps the 400ms default so lognav can always be quit. Non-zero values are clamped to [50, 2000]"`
	ScrollAxisLockMs        uint16      `yaml:"scrollAxisLockMs"        desc:"mouse-wheel axis lock window in milliseconds: within a continuous scroll gesture (including macOS momentum/inertial events) the wheel is locked to one axis, biased toward vertical, so a diagonal touchpad swipe scrolls vertically unless horizontal motion clearly dominates; sustained counter-axis scrolling reclaims control even mid-momentum. The lock resets after this many ms of scroll inactivity (0 disables locking, allowing mixed diagonal scroll)"`
	WheelScrollLines        uint16      `yaml:"wheelScrollLines"        desc:"number of rows a vertical mouse-wheel notch pans the viewport (log viewer and list panes scroll the view, not the cursor; the cursor/selection rides its line and is only dragged once it reaches the scroll margin). 0 uses the default of 5"`
	RedrawIntervalMs        uint16      `yaml:"redrawIntervalMs"        desc:"redraw tick interval in milliseconds while a fetch is in progress (lower = smoother live timer/log streaming, more CPU; clamped to 8-1000ms, 0 uses the 33ms default)"`
	SaveSnapshotOnFetchDone bool        `yaml:"saveSnapshotOnFetchDone" desc:"auto-save snapshot after fetch completes (skipped when every instance returned no logs)"`
	NotifyOnFetchDone       bool        `yaml:"notifyOnFetchDone"       desc:"notify when a fetch completes for all instances (mechanism set by notifyStyle)"`
	NotifyStyle             NotifyStyle `yaml:"notifyStyle"             desc:"how notifyOnFetchDone notifies: bell | osc9 (iTerm2/WezTerm/Ghostty/kitty) | osc777 (foot/urxvt/WezTerm/Ghostty) | osc99 (kitty/foot). osc* render only on a supporting terminal; inside tmux they also need 'allow-passthrough on'"`
	IncludeSnippetsInEditor bool        `yaml:"includeSnippetsInEditor" desc:"include snippets reference when opening external editor"`
	WatchCooldownSeconds    uint16      `yaml:"watchCooldownSeconds"    desc:"seconds to wait between watch fetch rounds (anti-DoS throttle)"`
	WatchMaxFetches         uint16      `yaml:"watchMaxFetches"         desc:"max watch fetch rounds including the first; 0 = unlimited"`
	WatchMaxDurationSeconds uint16      `yaml:"watchMaxDurationSeconds" desc:"max total watch wall-clock seconds before stopping; 0 = unlimited"`
	EnableExperimental      bool        `yaml:"enableExperimental"      desc:"enable experimental / opt-in features (currently: the archive background-query tab and fetch mode). Off by default."`
}

// RedrawInterval returns the bounded fetch-progress redraw cadence. Zero uses
// the default rather than disabling live progress.
func (c Core) RedrawInterval() time.Duration {
	ms := c.RedrawIntervalMs
	switch {
	case ms == 0:
		ms = 33
	case ms < 8:
		ms = 8
	case ms > 1000:
		ms = 1000
	}
	return time.Duration(ms) * time.Millisecond
}
