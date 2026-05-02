package flipper

import (
	"github.com/olivierpoupier/patch/tui/components"
)

// menuItemsFromCommands converts the command registry into the generic
// MenuItem list the components.MenuList consumes.
func menuItemsFromCommands(cmds []FlipperCommand) []components.MenuItem {
	items := make([]components.MenuItem, 0, len(cmds))
	for _, c := range cmds {
		items = append(items, components.MenuItem{
			ID:    c.ID,
			Label: c.Label,
			Group: c.Group,
		})
	}
	return items
}
