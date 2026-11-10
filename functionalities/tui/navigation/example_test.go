package navigation_test

import (
	"fmt"

	"st-go/tui/navigation"
)

func Example() {
	panel := navigation.DefaultPanel()
	selection := navigation.Selection{}
	selection.Select(7, 20, 5)
	fmt.Println(panel.PreviewWidth(80, 47), selection.Index, selection.Top)
	panel.TogglePin()
	fmt.Println(panel.PreviewWidth(80, 47))
	// Output:
	// 80 7 3
	// 45
}
