package main

import "st-go/tui/readline"

const maxPromptBytes = readline.DefaultLimit

func (e *Editor) syncPrompt() {
	e.prompt = e.promptEditor.Text()
	e.promptCursor = e.promptEditor.Cursor
	e.promptView = e.promptEditor.ViewStart()
}
func (e *Editor) beginPrompt(value string) {
	if e.promptEditor == nil {
		e.promptEditor = readline.New(value, 1, maxPromptBytes)
	} else {
		e.promptEditor.Reset(value, 1)
	}
	e.promptEditor.CancelEmptyBackspace = true
	e.promptEditor.SetHistory(e.promptHistory)
	e.promptStart = e.pos()
	e.syncPrompt()
}
func (e *Editor) cancelPrompt() {
	e.prompt = ""
	e.promptCursor, e.promptView = 0, 0
	e.substitute = nil
	e.reset()
}
func (e *Editor) insertPrompt(text string) {
	if e.prompt == "" {
		return
	}
	if err := e.promptEditor.Insert(text); err != nil {
		e.error(err)
		return
	}
	e.syncPrompt()
}
func (e *Editor) promptKey(key string) {
	if e.prompt == "" {
		return
	}
	result := e.promptEditor.Handle(key)
	e.syncPrompt()
	if result.Err != nil {
		e.error(result.Err)
	}
	if result.Cancel {
		e.cancelPrompt()
		return
	}
	if !result.Submit {
		return
	}
	prompt := e.prompt
	e.prompt = ""
	e.promptCursor, e.promptView = 0, 0
	e.promptHistory = append(e.promptHistory, prompt)
	if len(e.promptHistory) > 100 {
		e.promptHistory = e.promptHistory[1:]
	}
	if prompt[0] == ':' {
		e.execute(prompt[1:])
	} else {
		count := e.number()
		if e.operator != "" {
			count = min(maxCount, count*e.operatorCount)
		}
		e.startSearch(prompt[1:], prompt[0] == '?', false, count)
		if e.operator != "" {
			target := e.pos()
			e.window().cursor = e.promptStart
			e.applyMotionOperator(target, false, false)
		}
	}
	e.reset()
}
func (e *Editor) promptLine(width int) (string, int) {
	if e.prompt == "" {
		return "", 0
	}
	text, cursor := e.promptEditor.View(width, runeCells)
	e.syncPrompt()
	return text, cursor
}
