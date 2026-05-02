package flipper

import (
	"github.com/olivierpoupier/patch/tui/components"
)

// gpioMenuID is the menu ID for the entry that opens the GPIO sub-view.
// runSelectedMenuCommand recognises it specially and switches modes instead
// of issuing a CLI command.
const gpioMenuID = "gpio_open"

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

// menuItems returns the full menu list shown in the modal: the registered
// FlipperCommands plus any sub-view openers (currently just GPIO).
func menuItems() []components.MenuItem {
	items := menuItemsFromCommands(flipperCommands())
	items = append(items, components.MenuItem{
		ID:    gpioMenuID,
		Label: "GPIO pins",
		Group: "Hardware",
	})
	return items
}
