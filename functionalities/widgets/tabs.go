package widgets

import "st-go/platform_neutral/api"

func (h *Host) BindNewTerminalTab(handler func()) {
	if host, ok := h.window.native.(api.TerminalTabButtonHost); ok {
		host.BindNewTerminalTab(handler)
	}
}
