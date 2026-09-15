package filehandler

import (
	"github.com/bevicted/lognav/internal/ui/keys"
)

// DecorateHints returns file action bindings with context-local key-hint
// metadata. It copies the slice and each binding value so consumers can opt in
// without changing filehandler's complete Help bindings or another consumer.
func DecorateHints(bindings []keys.Binding) []keys.Binding {
	decorated := append([]keys.Binding(nil), bindings...)
	for i := range decorated {
		switch decorated[i].Context {
		case "select file":
			decorated[i] = decorated[i].WithHint("open", 2)
		case "rename file":
			decorated[i] = decorated[i].WithHint("rename", 3)
		case "delete file":
			decorated[i] = decorated[i].WithHint("delete", 4)
		}
	}
	return decorated
}

func bindKeyhandlersToModel(m *Model) {
	m.kh = keys.New().Bind(
		keys.Binding{
			Keys:    m.bundle.Config.Keys.Accept,
			Context: "select file",
			Action:  m.ReadFileUnderCursor,
		},
		keys.Binding{
			Keys:    m.bundle.Config.Keys.Delete,
			Context: "delete file",
			Action:  m.DeleteFileUnderCursor,
		},
		keys.Binding{
			Keys:    m.bundle.Config.Keys.Rename,
			Context: "rename file",
			Action:  m.RenameUnderCursor,
		},
	)
}
