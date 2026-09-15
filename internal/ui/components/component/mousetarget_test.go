package component_test

import (
	"testing"

	"github.com/bevicted/lognav/internal/ui/components/component"
	uv "github.com/charmbracelet/ultraviolet"
)

type mouseStub struct{}

func (mouseStub) OnMouseClick(x, y int, btn uv.MouseButton) bool { return false }

func TestMouseTarget_Stub_SatisfiesInterface(t *testing.T) {
	t.Parallel()
	var _ component.MouseTarget = mouseStub{}
}
