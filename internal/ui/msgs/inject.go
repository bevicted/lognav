package msgs

// InjectQueryMsg requests appending a Dataprime clause (e.g. "| filter ...") to
// the query editor and switching to the query tab. It is posted by the
// logviewer's inject-field dialog (the query destination) and consumed by the
// root ui.Model, which forwards it to the query editor. The query is staged, not
// run — the user reviews and executes the refetch deliberately.
type InjectQueryMsg struct {
	Snippet string
}

// SetEditorQueryMsg REPLACES the query editor's whole buffer with Query (unlike
// InjectQueryMsg, which appends a single filter clause). It is posted by the
// collect flow so the visible editor shows the archive's stored query — the
// editor owns its own buffer and only pushes to State, so setting State alone
// would not update what the user sees. The query is staged, not run; collect
// drives its own fetch separately. The root ui.Model forwards it to the editor.
type SetEditorQueryMsg struct {
	Query string
}
