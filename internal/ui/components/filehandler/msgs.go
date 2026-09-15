package filehandler

import (
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/bevicted/lognav/internal/ui/components/list"
)

type Operation int

const (
	ListOP Operation = iota
	ReadOP
	Preview
	WriteOP
	MoveOP
	DeleteOP
)

type IOMsg struct {
	id      uint64
	op      Operation
	err     error
	wrn     error
	files   []string
	styles  []uv.Style       // parallel to files; populated by ListFiles from FileOpts.NewRowStyler (zero Style = unstyled)
	preview [][]list.Segment // Preview: cell-native styled preview lines
}

func (m IOMsg) withErr(err error) IOMsg {
	m.err = err
	return m
}

func (m *Model) ioErr(err error) IOMsg {
	return IOMsg{
		id:  m.id,
		err: err,
	}
}
