package term

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"unicode"
	"unicode/utf8"

	"st-go/limits"
)

// RegionList is application-owned content for a retained, locally scrollable
// text panel. Tokens and item IDs are opaque: the terminal never opens source
// files or interprets application-specific navigation targets.
type RegionList struct {
	Token int              `json:"token"`
	Title string           `json:"title"`
	Items []RegionListItem `json:"items"`
}
type RegionListItem struct {
	ID    int    `json:"id"`
	Level int    `json:"level"`
	Text  string `json:"text"`
}
type RegionListHooks interface {
	// nil restores the retained image/PDF and its scroll position.
	ShowRegionList(RegionID, *RegionList) error
}

func (list RegionList) Validate() error {
	if list.Token < 1 || list.Token > 1<<30 || len(list.Items) > 4096 || !listText(list.Title, 256) {
		return fmt.Errorf("invalid region list")
	}
	seen := make(map[int]bool)
	bytes := len(list.Title)
	for _, item := range list.Items {
		bytes += len(item.Text)
		if bytes > limits.ControlBytes/2 {
			return fmt.Errorf("region list text exceeds resource limit")
		}
		if item.ID < 1 || item.ID > 1<<30 || seen[item.ID] || item.Level < 0 || item.Level > 8 || !listText(item.Text, 2048) {
			return fmt.Errorf("invalid region list item")
		}
		seen[item.ID] = true
	}
	return nil
}

func listText(text string, maximum int) bool {
	if len(text) > maximum || !utf8.ValidString(text) {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

func (t *Term) dslRegionList(id RegionID, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("region list requires base64 JSON or restore")
	}
	hooks, ok := t.hooks.(RegionListHooks)
	if !ok {
		return fmt.Errorf("retained text lists are not supported by this frontend")
	}
	if args[0] == "restore" {
		return hooks.ShowRegionList(id, nil)
	}
	if len(args[0]) > limits.ControlBytes-128 {
		return fmt.Errorf("region list exceeds resource limit")
	}
	data, err := base64.StdEncoding.DecodeString(args[0])
	if err != nil {
		return fmt.Errorf("invalid region list encoding")
	}
	var list RegionList
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("invalid region list JSON")
	}
	if err := list.Validate(); err != nil {
		return err
	}
	return hooks.ShowRegionList(id, &list)
}
